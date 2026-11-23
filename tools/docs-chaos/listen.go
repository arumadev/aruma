package main

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// listener holds a Listen stream on the query "every document of ops" over gRPC, as the
// mobile SDKs do. When the server dies it opens a new stream with the resume token of its last
// consistent snapshot, as the SDKs do, and checks that what comes back is exactly what it
// missed: at the first consistent snapshot after a resume, its view must equal the query's
// result at that snapshot's read time, and no document it already held at the same version
// may be sent again, unless the server said RESET.
type listener struct {
	r  *run
	id int

	mu   sync.Mutex
	view map[string]time.Time // name → update_time
	// undo holds, for each document changed since the last consistent snapshot, its version at
	// that snapshot (zero: absent). A new stream starts from the snapshot its token names, so
	// the view goes back to it first, as the SDKs drop what they had not raised.
	undo      map[string]time.Time
	token     []byte
	snapAt    time.Time // read time of the last consistent snapshot
	snapshots int
	resumes   int
	resets    int
	redeliver int // documents sent again at the version the view held, after a resume
	checked   int // snapshots compared with the query's result
	skipped   int // comparisons the server's death interrupted
	streams   int
	filters   int
}

const listenTarget = 1

func (l *listener) query() *pb.StructuredQuery {
	return &pb.StructuredQuery{From: []*pb.StructuredQuery_CollectionSelector{{CollectionId: "ops"}}}
}

func (l *listener) loop(ctx context.Context) {
	l.view = map[string]time.Time{}
	l.undo = map[string]time.Time{}
	for ctx.Err() == nil {
		ep, err := l.r.WaitUp(ctx)
		if err != nil {
			return
		}
		l.stream(ctx)
		if ctx.Err() != nil {
			return
		}
		_, _ = l.r.WaitNext(ctx, ep)
	}
}

// stream runs one Listen stream until it breaks.
func (l *listener) stream(ctx context.Context) {
	g := l.r.grpc
	s := g.c.Listen(ctx)
	s.RequestHeader().Set("Google-Cloud-Resource-Prefix", l.r.db)
	s.RequestHeader().Set("X-Goog-Request-Params", "database="+l.r.db)
	l.mu.Lock()
	for n, was := range l.undo {
		if was.IsZero() {
			delete(l.view, n)
		} else {
			l.view[n] = was
		}
	}
	clear(l.undo)
	token := slices.Clone(l.token)
	l.streams++
	resumed := token != nil
	if resumed {
		l.resumes++
	}
	l.mu.Unlock()
	t := &pb.Target{TargetId: listenTarget, TargetType: &pb.Target_Query{Query: &pb.Target_QueryTarget{
		Parent: l.r.db + "/documents", QueryType: &pb.Target_QueryTarget_StructuredQuery{StructuredQuery: l.query()}}}}
	if token != nil {
		t.ResumeType = &pb.Target_ResumeToken{ResumeToken: token}
	}
	if err := s.Send(&pb.ListenRequest{Database: l.r.db, TargetChange: &pb.ListenRequest_AddTarget{AddTarget: t}}); err != nil {
		return
	}
	defer s.CloseRequest()
	// A cancelled context alone does not always end a Receive blocked on an h2c stream.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = s.CloseResponse()
		case <-done:
		}
	}()
	// held is the view as it was when the stream opened: what a resume must not send again.
	l.mu.Lock()
	held := maps.Clone(l.view)
	l.mu.Unlock()
	reset := false
	afterResume := resumed
	current := false
	for {
		m, err := s.Receive()
		if err != nil {
			return
		}
		switch x := m.GetResponseType().(type) {
		case *pb.ListenResponse_TargetChange:
			tc := x.TargetChange
			switch tc.GetTargetChangeType() {
			case pb.TargetChange_RESET:
				l.mu.Lock()
				l.resets++
				for n, ut := range l.view {
					l.touch(n, ut)
				}
				clear(l.view)
				l.mu.Unlock()
				reset = true
			case pb.TargetChange_CURRENT:
				current = true
			case pb.TargetChange_REMOVE:
				l.r.Violation("listener %d: target removed: %v", l.id, tc.GetCause())
				return
			case pb.TargetChange_NO_CHANGE:
				if len(tc.GetTargetIds()) != 0 || tc.GetReadTime() == nil || !current {
					continue
				}
				rt := tc.GetReadTime().AsTime()
				l.mu.Lock()
				if rt.Before(l.snapAt) {
					l.r.Violation("listener %d: snapshot at %v, before the last at %v", l.id, rt, l.snapAt)
				}
				l.snapAt = rt
				l.snapshots++
				clear(l.undo)
				if len(tc.GetResumeToken()) > 0 {
					l.token = slices.Clone(tc.GetResumeToken())
				}
				view := maps.Clone(l.view)
				l.mu.Unlock()
				if afterResume {
					afterResume = false
					l.compare(ctx, rt, view, "first snapshot after a resume")
				}
				l.r.noteSnapshot(rt)
			}
		case *pb.ListenResponse_DocumentChange:
			dc := x.DocumentChange
			name, ut := dc.GetDocument().GetName(), dc.GetDocument().GetUpdateTime().AsTime()
			l.mu.Lock()
			if slices.Contains(dc.GetTargetIds(), listenTarget) {
				if afterResume && !reset {
					if was, ok := held[name]; ok && was.Equal(ut) {
						l.redeliver++
					}
				}
				l.set(name, ut)
			} else if slices.Contains(dc.GetRemovedTargetIds(), listenTarget) {
				l.set(name, time.Time{})
			}
			l.mu.Unlock()
		case *pb.ListenResponse_DocumentDelete:
			l.mu.Lock()
			l.set(x.DocumentDelete.GetDocument(), time.Time{})
			l.mu.Unlock()
		case *pb.ListenResponse_DocumentRemove:
			l.mu.Lock()
			l.set(x.DocumentRemove.GetDocument(), time.Time{})
			l.mu.Unlock()
		case *pb.ListenResponse_Filter:
			l.mu.Lock()
			l.filters++
			if n := int(x.Filter.GetCount()); n != len(l.view) {
				l.r.Violation("listener %d: existence filter counts %d documents, the view holds %d", l.id, n, len(l.view))
			}
			l.mu.Unlock()
		}
	}
}

// touch notes a document's version at the last snapshot before its first change since.
func (l *listener) touch(name string, was time.Time) {
	if _, ok := l.undo[name]; !ok {
		l.undo[name] = was
	}
}

// set changes a document of the view; zero removes it. l.mu is held.
func (l *listener) set(name string, ut time.Time) {
	l.touch(name, l.view[name])
	if ut.IsZero() {
		delete(l.view, name)
	} else {
		l.view[name] = ut
	}
}

// compare checks a view against the query's result at its read time.
func (l *listener) compare(ctx context.Context, rt time.Time, view map[string]time.Time, what string) {
	docs, _, err := l.r.grpc.Query(ctx, &pb.RunQueryRequest{Parent: l.r.db + "/documents",
		QueryType:           &pb.RunQueryRequest_StructuredQuery{StructuredQuery: l.query()},
		ConsistencySelector: &pb.RunQueryRequest_ReadTime{ReadTime: timestamppb.New(rt)}})
	if err != nil {
		l.mu.Lock()
		l.skipped++
		l.mu.Unlock()
		return
	}
	want := map[string]time.Time{}
	for _, d := range docs {
		want[d.GetName()] = d.GetUpdateTime().AsTime()
	}
	if diff := viewDiff(view, want); diff != "" {
		l.r.Violation("listener %d, %s at %v: %s", l.id, what, rt, diff)
	}
	l.mu.Lock()
	l.checked++
	l.mu.Unlock()
}

// finalCheck waits for a snapshot at or after after and compares it with the query's result.
func (l *listener) finalCheck(ctx context.Context, after time.Time) {
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		l.mu.Lock()
		at, view := l.snapAt, maps.Clone(l.view)
		l.mu.Unlock()
		if !at.Before(after) {
			l.compare(ctx, at, view, "final snapshot")
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	l.r.Violation("listener %d: no snapshot at or after %v within 20 s (last at %v)", l.id, after, l.snapAt)
}

func viewDiff(got, want map[string]time.Time) string {
	var out []string
	for _, n := range slices.Sorted(maps.Keys(want)) {
		g, ok := got[n]
		switch {
		case !ok:
			out = append(out, "missing "+n)
		case !g.Equal(want[n]):
			out = append(out, fmt.Sprintf("%s at %v, want %v", n, g, want[n]))
		}
	}
	for _, n := range slices.Sorted(maps.Keys(got)) {
		if _, ok := want[n]; !ok {
			out = append(out, "extra "+n)
		}
	}
	if len(out) > 8 {
		out = append(out[:8], fmt.Sprintf("and %d more", len(out)-8))
	}
	if len(out) == 0 {
		return ""
	}
	return fmt.Sprint(out)
}
