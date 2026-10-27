package api

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"slices"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/arumadev/aruma/internal/services/documents/core"
	"github.com/arumadev/aruma/internal/services/documents/listen"
	"github.com/arumadev/aruma/internal/services/documents/query"
	"github.com/arumadev/aruma/internal/services/documents/wire"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// The Listen stream's protocol (ADR-026 §1), on the hub of package listen, as production answers
// it (f4-listen.json; the case ids below are its own). The transport (package grpc) owns the
// stream's lifetime and deadlines, reads the client's requests, and sends what Serve gives it.
//
//   - Each add_target is answered ADD[id], its documents, CURRENT[id] with its read time and
//     token, then, once every target of the stream has reached a commit, a global NO_CHANGE (no
//     target ids) at it (listen.doc-target, listen.two-targets). A document that does not exist
//     is a document_delete before CURRENT (listen.doc-missing). once is ADD, the result, CURRENT,
//     the global snapshot, REMOVE (refused-targets streams 24, 25).
//   - Each remove_target is REMOVE[id], no cause.
//   - A commit is sent whole: one document_change per document with every target it is in
//     (target_ids) or left by a change (removed_target_ids, with the new body), a document_delete
//     for one deleted, then the global snapshot at its commit (listen.query-target,
//     listen.two-targets).
//   - A target with a limit keeps its window: a document pushed out by one that enters is a
//     document_remove beside the document_change; a member deleted or moved out is RESET, the
//     whole window, CURRENT (listen.limit-target). A target with an offset is read again on every
//     change of its documents, and RESET whenever a document leaves its result.
//   - A target that cannot be served is REMOVE[id] with a cause, and the stream stays open: alone
//     for what the request itself gets wrong; after ADD for what the database refuses (a missing
//     index, find_nearest, a read_time in the future). The client's mistakes in the stream itself
//     end it with a status.
//   - Resume (resume_token or read_time T): the documents of the result changed after T; within
//     the changelog, a document_delete for each one deleted since; for a query, ExistenceFilter
//     with the exact count, which the client checks and, when it differs, queries again; then
//     CURRENT (listen.resume-token, listen.resume-read-time). A limit or offset target is
//     compared with its result at T, or RESET when T is older than the versions kept.
//
// Production repeats global snapshots and query filters unasked, as heartbeats; this server
// sends neither (parity exception listen-heartbeats), nor a bloom filter (listen-no-bloom).

// ListenStream is one open Listen stream, admitted at its first request. It is not safe for
// concurrent use: Serve runs it on one goroutine.
type ListenStream struct {
	s    *Service
	a    admitted
	sub  *listen.Subscriber
	send func(*pb.ListenResponse) error
	lim  ListenLimits

	targets  map[int32]*listenTarget
	byTarget map[*listen.Target]*listenTarget
	maxID    int32
	assigned bool // a target id was given by the server: the client may give none after
	// dirty is something sent since the last global snapshot, which the next one covers.
	dirty bool
}

// listenTarget is a target of the stream.
type listenTarget struct {
	id int32
	t  *listen.Target
	// snap is the commit its last CURRENT is at: the global snapshot waits for it.
	snap int64
	once bool
	// paths are a document target's documents, in the request's order; nil for a query.
	paths []core.Path
	// q is a query's; windowed, a query with a limit or an offset, whose result is window.
	q        *query.Query
	windowed bool
	window   []core.Document
}

// OpenListen admits the caller to the database the stream's first request names, which must be
// the one its headers name, and subscribes the stream to its commits.
func (s *Service) OpenListen(ctx context.Context, c Call, first *pb.ListenRequest) (*ListenStream, error) {
	if s.o.Listen == nil {
		return nil, errListenUnserved
	}
	a, err := s.admit(ctx, c, &pb.ListenRequest{Database: first.GetDatabase()}, databaseOf(first.GetDatabase()))
	if err != nil {
		return nil, err
	}
	if h := c.headerDatabase(); h != "" && h != a.wdb.Name() {
		return nil, errListenDatabase
	}
	if _, err := s.o.Store.Database(ctx, a.db); err != nil {
		return nil, s.fail(ctx, err)
	}
	sub, err := s.o.Listen.Subscribe(ctx, a.db)
	if errors.Is(err, listen.ErrClosed) {
		return nil, errListenClosed
	}
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	return &ListenStream{s: s, a: a, sub: sub, targets: map[int32]*listenTarget{}, byTarget: map[*listen.Target]*listenTarget{}}, nil
}

// Close ends the stream's subscription.
func (l *ListenStream) Close() { l.sub.Close() }

// ListenLimits are a Listen stream's own bounds (ADR-026 §3), which the transport sets before
// Serve; a zero field is no bound.
type ListenLimits struct {
	// Targets is targets_per_stream: one more target is removed with RESOURCE_EXHAUSTED.
	Targets int
	// Idle is stream_idle: a stream without a target that sent nothing for it ends UNAVAILABLE.
	Idle time.Duration
	// Overflow, when set, is told of each bound reached, by its setting's name, for the
	// transport's metric and log line: targets_per_stream, snapshot_rows, stream_lag_events.
	Overflow func(limit string)
}

// Limit sets the stream's bounds.
func (l *ListenStream) Limit(lim ListenLimits) { l.lim = lim }

// Admission is who the stream was admitted for.
func (l *ListenStream) Admission() Admission { return l.a.public() }

func (l *ListenStream) overflow(limit string) {
	if l.lim.Overflow != nil {
		l.lim.Overflow(limit)
	}
}

// Serve answers first, then each request of requests until it closes (the client ended its side)
// and every commit of the stream's targets, through send, until ctx ends or the stream fails.
// An error ends the stream with its status.
func (l *ListenStream) Serve(ctx context.Context, first *pb.ListenRequest, requests <-chan *pb.ListenRequest,
	send func(*pb.ListenResponse) error) error {
	l.send = send
	// idle runs while the stream has no target, from its last request (stream_idle).
	idle := time.NewTimer(time.Hour)
	idle.Stop()
	defer idle.Stop()
	rearm := func() {
		if l.lim.Idle > 0 && len(l.targets) == 0 {
			idle.Reset(l.lim.Idle)
		} else {
			idle.Stop()
		}
	}
	if err := l.handle(ctx, first); err != nil {
		return err
	}
	rearm()
	for {
		select {
		case req, ok := <-requests:
			if !ok {
				return nil
			}
			if err := l.handle(ctx, req); err != nil {
				return err
			}
			rearm()
		case <-l.sub.Ready():
			had := len(l.targets)
			if err := l.deliver(ctx, l.sub.Take()); err != nil {
				return err
			}
			if had > 0 && len(l.targets) == 0 {
				rearm() // the last target went: a once target, or one past snapshot_rows
			}
		case <-idle.C:
			return errListenIdle
		case <-l.sub.Done():
			return errListenClosed
		case <-ctx.Done():
			return context.Cause(ctx)
		}
	}
}

// handle answers one request.
func (l *ListenStream) handle(ctx context.Context, req *pb.ListenRequest) error {
	if d := req.GetDatabase(); d != "" && d != l.a.wdb.Name() {
		return errListenDatabase
	}
	switch tc := req.GetTargetChange().(type) {
	case *pb.ListenRequest_AddTarget:
		return l.add(ctx, tc.AddTarget)
	case *pb.ListenRequest_RemoveTarget:
		lt, ok := l.targets[tc.RemoveTarget]
		if !ok {
			return errListenNoTarget(tc.RemoveTarget)
		}
		l.forget(lt)
		return l.send(removeTarget(lt.id, nil))
	}
	return errListenNoChange
}

// add answers an add_target (package doc).
func (l *ListenStream) add(ctx context.Context, t *pb.Target) error {
	id := t.GetTargetId()
	switch {
	case id < 0:
		return errListenTargetID(id)
	case id == 0:
		// The server's: 1 on a new stream, else one past the highest (listen.remove-target).
		id, l.assigned = l.maxID+1, true
	case l.assigned:
		return errListenManualID
	}
	if _, ok := l.targets[id]; ok {
		return errListenDuplicate(id)
	}
	l.maxID = max(l.maxID, id)
	if l.lim.Targets > 0 && len(l.targets) >= l.lim.Targets {
		l.overflow("targets_per_stream")
		return l.send(removeTarget(id, errTooManyTargets(l.lim.Targets)))
	}

	lt, spec, err := l.parseTarget(id, t)
	if err != nil {
		return l.send(removeTarget(id, wire.From(err)))
	}
	var since time.Time
	resumed := false
	switch r := t.GetResumeType().(type) {
	case *pb.Target_ResumeToken:
		if len(r.ResumeToken) > 0 {
			var ok bool
			if since, ok = parseResumeToken(r.ResumeToken); !ok || since.After(time.Now()) {
				return l.send(removeTarget(id, errBadResumeToken))
			}
			resumed = !since.IsZero()
		}
	case *pb.Target_ReadTime:
		since, resumed = r.ReadTime.AsTime(), true
	}
	if err := l.send(targetChange(pb.TargetChange_ADD, id)); err != nil {
		return err
	}
	// What the database refuses comes after ADD (refused-targets streams 3, 8, 14;
	// listen.resume-read-time stream 7).
	if lt.q != nil {
		if err := l.s.checkQuery(ctx, l.a, lt.q, nil, false); err != nil {
			return l.send(removeTarget(id, wire.From(err)))
		}
	}
	if resumed && since.After(time.Now()) {
		return l.send(removeTarget(id, errTargetReadFuture))
	}

	if lt.t, err = l.sub.Add(spec); err != nil {
		if errors.Is(err, listen.ErrClosed) {
			return errListenClosed
		}
		return err
	}
	l.targets[id], l.byTarget[lt.t] = lt, lt
	if resumed {
		var expected *int32
		if e := t.GetExpectedCount(); e != nil {
			expected = new(e.GetValue())
		}
		err = l.resume(ctx, lt, since, expected)
	} else {
		err = l.snapshot(ctx, lt, false)
	}
	return l.targetFailed(lt, err)
}

// targetFailed ends one target on err, a *wire.Error (a read past snapshot_rows): REMOVE with it
// as the cause. Any other error ends the stream.
func (l *ListenStream) targetFailed(lt *listenTarget, err error) error {
	we, ok := errors.AsType[*wire.Error](err)
	if !ok {
		return l.s.fail(context.Background(), err)
	}
	if we.Code == wire.CodeResourceExhausted {
		l.overflow("snapshot_rows")
	}
	l.forget(lt)
	return l.send(removeTarget(lt.id, we))
}

// forget takes a target off the stream.
func (l *ListenStream) forget(lt *listenTarget) {
	l.sub.Remove(lt.t)
	delete(l.targets, lt.id)
	delete(l.byTarget, lt.t)
}

// parseTarget checks a target as production does before ADD (refused-targets): its names, which
// must be of the stream's database, and its query, which Listen serves only of one collection id
// without a projection. An error is the target's cause.
func (l *ListenStream) parseTarget(id int32, t *pb.Target) (*listenTarget, listen.Spec, error) {
	lt := &listenTarget{id: id, once: t.GetOnce()}
	spec := listen.Spec{ID: id}
	switch tt := t.GetTargetType().(type) {
	case *pb.Target_Documents:
		names := tt.Documents.GetDocuments()
		if len(names) == 0 {
			return nil, spec, errTargetNoDocuments
		}
		seen := map[string]bool{}
		for _, s := range names {
			n, err := wire.ParseName(s)
			switch {
			case err != nil:
				return nil, spec, err
			case n.Database != l.a.wdb:
				return nil, spec, errTargetDatabase
			case n.IsRoot():
				_, err := wire.ParseDocumentName(s)
				return nil, spec, cmp.Or(err, error(errTargetType))
			case !n.IsDocument():
				return nil, spec, errTargetPathPiece(n.ID())
			}
			if p := pathOf(n); !seen[p.String()] {
				seen[p.String()] = true
				lt.paths = append(lt.paths, p)
			}
		}
		spec.Documents = lt.paths
	case *pb.Target_Query:
		n, err := wire.ParseParentName(tt.Query.GetParent())
		if err != nil {
			return nil, spec, err
		}
		if n.Database != l.a.wdb {
			return nil, spec, errTargetDatabase
		}
		sq := tt.Query.GetStructuredQuery()
		if from := sq.GetFrom(); len(from) == 1 {
			switch {
			case from[0].GetCollectionId() == "":
				return nil, spec, errTargetAllKinds
			case from[0].GetAllDescendants() && !n.IsRoot():
				return nil, spec, errTargetGroupUnder
			}
		}
		if sq.GetSelect() != nil {
			return nil, spec, errTargetSelect
		}
		q, err := query.Parse(sq, n)
		if err != nil {
			if we, ok := errors.AsType[*wire.Error](err); ok && we.Message == msgEmptyPath {
				return nil, spec, errTargetIdentifier
			}
			return nil, spec, err
		}
		if q.CollectionID == "" {
			return nil, spec, errTargetAllKinds
		}
		lt.q, lt.windowed = q, q.Limit >= 0 || q.Offset > 0
		spec.Parent, spec.Query = pathOf(n), q
	default:
		return nil, spec, errTargetType
	}
	return lt, spec, nil
}

// snapshot sends a target's whole result and CURRENT; after RESET when reset.
func (l *ListenStream) snapshot(ctx context.Context, lt *listenTarget, reset bool) error {
	if reset {
		r := targetChange(pb.TargetChange_RESET, lt.id)
		r.GetTargetChange().ResumeToken = resetToken
		if err := l.send(r); err != nil {
			return err
		}
	}
	found := map[string]core.Document{}
	lt.window = lt.window[:0]
	head, err := lt.t.Snapshot(ctx, func(d core.Document) error {
		switch {
		case lt.paths != nil:
			found[d.Path.String()] = d
			return nil
		case lt.windowed:
			lt.window = append(lt.window, d)
		}
		return l.send(l.change(d, []int32{lt.id}, nil))
	})
	if err != nil {
		return err
	}
	if err := l.documents(lt, found, head, time.Time{}); err != nil {
		return err
	}
	return l.current(lt, head)
}

// documents sends a document target's documents found at head, in the request's order, those
// changed after since: a document that does not exist is a document_delete (listen.doc-missing,
// refused-targets stream 21).
func (l *ListenStream) documents(lt *listenTarget, found map[string]core.Document, head listen.Head, since time.Time) error {
	for _, p := range lt.paths {
		d, ok := found[p.String()]
		var err error
		switch {
		case !ok:
			err = l.send(l.deleted(p, head.ReadTime, []int32{lt.id}))
		case d.UpdateTime.After(since):
			err = l.send(l.change(d, []int32{lt.id}, nil))
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// current sends CURRENT at head: the target is consistent there, and the next global snapshot
// waits for it.
func (l *ListenStream) current(lt *listenTarget, head listen.Head) error {
	lt.snap, l.dirty = head.Seq, true
	return l.send(snapshotChange(pb.TargetChange_CURRENT, head.ReadTime, lt.id))
}

// resume sends what changed in a target's result since a time (package doc).
func (l *ListenStream) resume(ctx context.Context, lt *listenTarget, since time.Time, expected *int32) error {
	if lt.windowed {
		return l.resumeWindow(ctx, lt, since)
	}
	found := map[string]core.Document{}
	count, sent := 0, 0
	head, err := lt.t.Snapshot(ctx, func(d core.Document) error {
		if lt.paths != nil {
			found[d.Path.String()] = d
			return nil
		}
		if count++; d.UpdateTime.After(since) {
			sent++
			return l.send(l.change(d, []int32{lt.id}, nil))
		}
		return nil
	})
	if err != nil {
		return err
	}
	if lt.paths != nil {
		// A document target's answer is exact without a filter, as production's (no filter,
		// listen.resume-read-time streams 8, 9).
		if err := l.documents(lt, found, head, since); err != nil {
			return err
		}
		return l.current(lt, head)
	}
	deleted := 0
	if _, err := lt.t.Changes(ctx, listen.Head{ReadTime: since}, head, func(c listen.TargetChange) error {
		if c.Kind != listen.LeaveDeleted {
			return nil
		}
		deleted++
		return l.send(l.deleted(c.After.Path, head.ReadTime, []int32{lt.id}))
	}); err != nil {
		return err
	}
	// The count, unless the client's own count proves it right: nothing but deletes sent, and
	// what it held less those is the count (listen.resume-token streams 8, 12). A document sent
	// may be one it held or a new one, so then only the count tells (streams 2, 10).
	if expected == nil || sent > 0 || int(*expected)-deleted != count {
		f := &pb.ExistenceFilter{TargetId: lt.id, Count: int32(count)}
		if err := l.send(&pb.ListenResponse{ResponseType: &pb.ListenResponse_Filter{Filter: f}}); err != nil {
			return err
		}
	}
	return l.current(lt, head)
}

// resumeWindow resumes a target with a limit or an offset: what entered or changed, when nothing
// left its result since; else RESET and the whole result, as when the result at since is no
// longer kept (ADR-026 §1.5).
func (l *ListenStream) resumeWindow(ctx context.Context, lt *listenTarget, since time.Time) error {
	var old []core.Document
	err := lt.t.ReadAt(ctx, listen.Head{ReadTime: since}, func(d core.Document) error {
		old = append(old, d)
		return nil
	})
	if errors.Is(err, core.ErrReadTimeTooOld) {
		return l.snapshot(ctx, lt, true)
	}
	if err != nil {
		return err
	}
	var now []core.Document
	head, err := lt.t.Snapshot(ctx, func(d core.Document) error {
		now = append(now, d)
		return nil
	})
	if err != nil {
		return err
	}
	lt.window = now
	d := diffWindow(old, now)
	if len(d.left) > 0 {
		return l.reset(lt, head)
	}
	for _, doc := range d.changed {
		if err := l.send(l.change(doc, []int32{lt.id}, nil)); err != nil {
			return err
		}
	}
	return l.current(lt, head)
}

// reset sends RESET, the target's window and CURRENT at head.
func (l *ListenStream) reset(lt *listenTarget, head listen.Head) error {
	r := targetChange(pb.TargetChange_RESET, lt.id)
	r.GetTargetChange().ResumeToken = resetToken
	if err := l.send(r); err != nil {
		return err
	}
	for _, d := range lt.window {
		if err := l.send(l.change(d, []int32{lt.id}, nil)); err != nil {
			return err
		}
	}
	return l.current(lt, head)
}

// windowDiff is how a window changed: the documents that left it, and those that entered it or
// changed in it, in the new window's order.
type windowDiff struct {
	left    []core.Document
	changed []core.Document
}

func diffWindow(old, now []core.Document) windowDiff {
	was := make(map[string]core.Document, len(old))
	for _, d := range old {
		was[d.Path.String()] = d
	}
	var out windowDiff
	for _, d := range now {
		p := d.Path.String()
		if o, ok := was[p]; !ok || !o.UpdateTime.Equal(d.UpdateTime) {
			out.changed = append(out.changed, d)
		}
		delete(was, p)
	}
	for _, d := range old {
		if _, ok := was[d.Path.String()]; ok {
			out.left = append(out.left, d)
		}
	}
	return out
}

// deliver sends what the hub gave the stream, and the global snapshots it allows.
func (l *ListenStream) deliver(ctx context.Context, b listen.Batch) error {

	if b.Resync {
		// The hub lost commits the stream needed: every target's result again (ADR-026 §1.4).
		l.overflow("stream_lag_events")
		for _, id := range slices.Sorted(maps.Keys(l.targets)) {
			lt := l.targets[id]
			if err := l.targetFailed(lt, l.snapshot(ctx, lt, true)); err != nil {
				return err
			}
		}
		return nil
	}
	for _, e := range b.Events {
		if err := l.event(ctx, e); err != nil {
			return err
		}
		if err := l.global(e.Seq, e.CommitTime); err != nil {
			return err
		}
	}
	return l.global(b.Through.Seq, b.Through.ReadTime)
}

// global sends the global snapshot at a commit when something was sent since the last one and
// every target has reached the commit (ADR-026 §1.2); then a once target is done.
func (l *ListenStream) global(seq int64, rt time.Time) error {
	if !l.dirty {
		return nil
	}
	for _, lt := range l.targets {
		if lt.snap > seq {
			return nil
		}
	}
	l.dirty = false
	if err := l.send(snapshotChange(pb.TargetChange_NO_CHANGE, rt)); err != nil {
		return err
	}
	for _, id := range slices.Sorted(maps.Keys(l.targets)) {
		if lt := l.targets[id]; lt.once {
			l.forget(lt)
			if err := l.send(removeTarget(id, nil)); err != nil {
				return err
			}
		}
	}
	return nil
}

// docChange is what one commit did to one document for the stream's targets.
type docChange struct {
	after   core.Document
	deleted bool
	in      []int32 // target_ids: the targets it is in
	out     []int32 // removed_target_ids: those it left
}

// event sends one commit: per document, one message for all the targets it changed, then the
// windows reset (package doc).
func (l *ListenStream) event(ctx context.Context, e listen.Event) error {
	var order []string
	docs := map[string]*docChange{}
	at := func(p core.Path) *docChange {
		k := p.String()
		dc := docs[k]
		if dc == nil {
			dc = &docChange{}
			docs[k] = dc
			order = append(order, k)
		}
		return dc
	}
	windows := map[*listenTarget][]listen.TargetChange{}
	for _, c := range e.Changes {
		lt := l.byTarget[c.Target]
		switch {
		case lt == nil:
		case lt.windowed:
			windows[lt] = append(windows[lt], c)
		default:
			dc := at(c.After.Path)
			switch c.Kind {
			case listen.Enter, listen.Modify:
				dc.after, dc.in = c.After, append(dc.in, lt.id)
			case listen.LeaveExists:
				dc.after, dc.out = c.After, append(dc.out, lt.id)
			case listen.LeaveDeleted:
				dc.deleted, dc.out = true, append(dc.out, lt.id)
			}
		}
	}
	head := listen.Head{Seq: e.Seq, ReadTime: e.CommitTime}
	var removed []*pb.ListenResponse
	var resets []*listenTarget
	for _, lt := range slices.SortedFunc(maps.Keys(windows), func(a, b *listenTarget) int { return cmp.Compare(a.id, b.id) }) {
		d, ok, err := l.window(ctx, lt, head, windows[lt])
		switch {
		case err != nil:
			if err := l.targetFailed(lt, err); err != nil {
				return err
			}
			continue
		case !ok:
			resets = append(resets, lt)
			continue
		}
		for _, doc := range d.left {
			removed = append(removed, &pb.ListenResponse{ResponseType: &pb.ListenResponse_DocumentRemove{DocumentRemove: &pb.DocumentRemove{
				Document: l.a.db.DocumentName(doc.Path), RemovedTargetIds: []int32{lt.id}, ReadTime: timestamppb.New(e.CommitTime)}}})
		}
		for _, doc := range d.changed {
			dc := at(doc.Path)
			dc.after, dc.in = doc, append(dc.in, lt.id)
		}
	}
	// A document pushed out of a window goes before the one that pushed it (listen.limit-target).
	for _, r := range removed {
		if err := l.send(r); err != nil {
			return err
		}
	}
	for _, k := range order {
		dc := docs[k]
		slices.Sort(dc.in)
		slices.Sort(dc.out)
		var r *pb.ListenResponse
		if dc.deleted {
			r = l.deleted(core.ParsePath(k), e.CommitTime, dc.out)
		} else {
			r = l.change(dc.after, dc.in, dc.out)
		}
		if err := l.send(r); err != nil {
			return err
		}
		l.dirty = true
	}
	l.dirty = l.dirty || len(removed) > 0
	for _, lt := range resets {
		if err := l.reset(lt, head); err != nil {
			return err
		}
	}
	return nil
}

// window reads a windowed target's result again at a commit that changed some of its documents,
// and tells how it changed; ok is false when the target must be RESET: a member left it by its
// own change, or any document left a target with an offset.
func (l *ListenStream) window(ctx context.Context, lt *listenTarget, head listen.Head, changes []listen.TargetChange) (windowDiff, bool, error) {
	if !l.touches(lt, changes) {
		return windowDiff{}, true, nil
	}
	var now []core.Document
	if err := lt.t.ReadAt(ctx, head, func(d core.Document) error {
		now = append(now, d)
		return nil
	}); err != nil {
		return windowDiff{}, false, err
	}
	d := diffWindow(lt.window, now)
	lt.window = now
	if len(d.left) == 0 {
		return d, true, nil
	}
	if lt.q.Offset > 0 {
		return d, false, nil
	}
	changed := map[string]bool{}
	for _, c := range changes {
		changed[c.After.Path.String()] = true
	}
	for _, doc := range d.left {
		if changed[doc.Path.String()] {
			return d, false, nil
		}
	}
	return d, true, nil
}

// touches reports whether a commit's changes can change a window: any change for an offset; for
// a limit, a member changed, or a document now admitted that does not sort past a full window's
// last member.
func (l *ListenStream) touches(lt *listenTarget, changes []listen.TargetChange) bool {
	q := lt.q
	members := make(map[string]bool, len(lt.window))
	for _, d := range lt.window {
		members[d.Path.String()] = true
	}
	if q.Offset > 0 {
		// Any change before the window shifts it.
		return len(changes) > 0
	}
	full := len(lt.window) >= q.Limit
	for _, c := range changes {
		if members[c.After.Path.String()] {
			return true
		}
		if c.Kind != listen.Enter && c.Kind != listen.Modify {
			continue
		}
		if !full || q.Limit > 0 && q.Compare(l.queryDoc(c.After), l.queryDoc(lt.window[len(lt.window)-1])) < 0 {
			return true
		}
	}
	return false
}

func (l *ListenStream) queryDoc(d core.Document) *query.Doc {
	return &query.Doc{Name: l.a.db.DocumentName(d.Path), Fields: d.Fields, CreateTime: d.CreateTime, UpdateTime: d.UpdateTime}
}

// change is a document_change.
func (l *ListenStream) change(d core.Document, in, out []int32) *pb.ListenResponse {
	return &pb.ListenResponse{ResponseType: &pb.ListenResponse_DocumentChange{DocumentChange: &pb.DocumentChange{
		Document:  wire.DocumentToProto(l.a.db.DocumentName(d.Path), d.Fields, timestamp(d.CreateTime), timestamp(d.UpdateTime)),
		TargetIds: in, RemovedTargetIds: out}}}
}

// deleted is a document_delete at rt.
func (l *ListenStream) deleted(p core.Path, rt time.Time, out []int32) *pb.ListenResponse {
	return &pb.ListenResponse{ResponseType: &pb.ListenResponse_DocumentDelete{DocumentDelete: &pb.DocumentDelete{
		Document: l.a.db.DocumentName(p), RemovedTargetIds: out, ReadTime: timestamppb.New(rt)}}}
}
