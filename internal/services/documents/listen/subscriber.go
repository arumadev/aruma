package listen

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/arumadev/aruma/internal/services/documents/core"
	"github.com/arumadev/aruma/internal/services/documents/query"
)

// Head is a commit of a database: its seq and its commit_time, the read_time of a result at it.
type Head struct {
	Seq      int64
	ReadTime time.Time
}

// Kind is what a commit did to a document of a target (ADR-026 §2's table).
type Kind uint8

const (
	// Enter: the document did not match before the commit and matches after
	// (document_change with target_ids).
	Enter Kind = iota + 1
	// Modify: it matched and still matches, changed (document_change).
	Modify
	// LeaveExists: it matched and exists after without matching (document_change with
	// removed_target_ids).
	LeaveExists
	// LeaveDeleted: it matched and was deleted (document_delete with removed_target_ids).
	LeaveDeleted
)

// TargetChange is one document a commit changed for one target: its images before and after
// (a document that does not exist has its Path alone).
type TargetChange struct {
	Target        *Target
	Kind          Kind
	Before, After core.Document
}

// Event is one commit's changes to a subscriber's targets: all of them, never part.
type Event struct {
	Seq        int64
	CommitTime time.Time
	Changes    []TargetChange
}

// Batch is what Take returns.
type Batch struct {
	// Events are the commits that changed the subscriber's targets, in seq order, past each
	// target's snapshot.
	Events []Event
	// Through is a commit every event up to which the subscriber has now been given: at least
	// the last event's.
	Through Head
	// Resync reports that the subscriber fell behind (LagEvents), or the hub lost commits to
	// retention: it must take every target's result again (Target.Snapshot) before it takes
	// events again; the events up to then are dropped (ADR-026 §1.4, cases 2 and 3).
	Resync bool
}

// Subscriber is one stream's subscription to one database's commits. Its targets' events
// queue in commit order, at most LagEvents of them, until Take; Ready signals that there is
// something to take. Its methods may be called from any goroutine, but a target's Snapshot
// must return before the events that follow are taken (A2's one stream goroutine does both).
type Subscriber struct {
	h     *Hub
	d     *database
	db    core.DB
	ready chan struct{}
	done  chan struct{}
	stop  sync.Once

	mu      sync.Mutex
	queue   []Event
	resync  bool
	closed  bool
	targets map[*Target]struct{}
}

func newSubscriber(h *Hub, d *database, db core.DB) *Subscriber {
	return &Subscriber{h: h, d: d, db: db, ready: make(chan struct{}, 1), done: make(chan struct{}),
		targets: map[*Target]struct{}{}}
}

// Ready signals that Take has something: events, a resync, or the position Await asked for.
func (s *Subscriber) Ready() <-chan struct{} { return s.ready }

// Done closes when the subscriber is closed, by Close or by the hub's.
func (s *Subscriber) Done() <-chan struct{} { return s.done }

func (s *Subscriber) signal() {
	select {
	case s.ready <- struct{}{}:
	default:
	}
}

// Take returns the events queued, and the position they bring the subscriber to. It does not
// block.
func (s *Subscriber) Take() Batch {
	// The position first: every event up to it was queued before it was set.
	pos := *s.d.pos.Load()
	s.mu.Lock()
	defer s.mu.Unlock()
	b := Batch{Through: pos}
	if s.resync {
		s.resync = false
		b.Resync = true
		return b
	}
	for _, e := range s.queue {
		e.Changes = slices.DeleteFunc(e.Changes, func(c TargetChange) bool { return !c.Target.wants(e.Seq) })
		if len(e.Changes) > 0 {
			b.Events = append(b.Events, e)
		}
		if e.Seq > b.Through.Seq {
			b.Through = Head{Seq: e.Seq, ReadTime: e.CommitTime}
		}
	}
	s.queue = nil
	return b
}

// Await asks Ready to signal once the hub has released seq: a stream's global snapshot at a
// commit waits for every target to reach it (ADR-026 §1.2). The latest seq asked wins; Snapshot
// asks for its own.
func (s *Subscriber) Await(seq int64) {
	d := s.d
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.pos.Load().Seq >= seq {
		s.signal()
		return
	}
	d.waiting[s] = max(d.waiting[s], seq)
}

// push queues an event; d.mu is held.
func (s *Subscriber) push(e Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.resync {
		return
	}
	e.Changes = slices.DeleteFunc(e.Changes, func(c TargetChange) bool { return !c.Target.wants(e.Seq) })
	if len(e.Changes) == 0 {
		return
	}
	if len(s.queue) >= s.h.o.LagEvents {
		s.h.o.Log.Warn("documents: a listener fell behind the hub; it resyncs",
			"project", s.db.Project, "database", s.db.ID, "events", len(s.queue))
		s.queue, s.resync = nil, true
		s.signal()
		return
	}
	s.queue = append(s.queue, e)
	s.signal()
}

func (s *Subscriber) markResync() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.queue, s.resync = nil, true
		s.signal()
	}
}

// end closes the subscriber without leaving its database: the hub is closing.
func (s *Subscriber) end() {
	s.mu.Lock()
	s.closed, s.queue = true, nil
	s.mu.Unlock()
	s.stop.Do(func() { close(s.done) })
}

// Close ends the subscription and removes its targets.
func (s *Subscriber) Close() {
	s.mu.Lock()
	targets := s.targets
	s.targets = map[*Target]struct{}{}
	s.mu.Unlock()
	s.end()
	s.h.leave(s)
	d := s.d
	d.mu.Lock()
	for t := range targets {
		d.targets.remove(t)
	}
	delete(d.waiting, s)
	d.mu.Unlock()
}

// Spec is what a target listens to: a query's result under a parent, or a set of documents.
type Spec struct {
	// ID is the client's target_id, carried for the caller.
	ID int32
	// Parent and Query are a query target's: the query as query.Parse checked it, under the
	// documents root (nil) or a document. Its offset and limit bound Snapshot's result; the
	// events judge membership by filter, order fields and cursors alone (query.Admits), and the
	// caller keeps a limit window.
	Parent core.Path
	Query  *query.Query
	// Documents is a document target's: its documents' paths.
	Documents []core.Path
}

// Target is one target of a subscriber.
type Target struct {
	Spec
	sub *Subscriber
	// from is the hub's position when the target was added.
	from int64
	// docs is a document target's paths, by String.
	docs map[string]bool

	// Guarded by sub.mu: skip is the snapshot's seq, whose events and those before it the
	// snapshot holds; removed is a target taken off.
	skip    int64
	removed bool
}

// ErrBadSpec is a Spec with both a query and documents, or neither.
var ErrBadSpec = errors.New("listen: a target is a query or documents")

// Add registers a target at the hub's current position: every commit released after it is
// judged for it. Take its result with Snapshot next.
func (s *Subscriber) Add(spec Spec) (*Target, error) {
	if (spec.Query == nil) == (len(spec.Documents) == 0) {
		return nil, ErrBadSpec
	}
	t := &Target{Spec: spec, sub: s}
	if spec.Query == nil {
		t.docs = map[string]bool{}
		for _, p := range spec.Documents {
			t.docs[p.String()] = true
		}
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrClosed
	}
	s.targets[t] = struct{}{}
	s.mu.Unlock()
	d := s.d
	d.mu.Lock()
	d.targets.add(t)
	t.from = d.pos.Load().Seq
	d.mu.Unlock()
	return t, nil
}

// Remove takes a target off: no event of it is taken after.
func (s *Subscriber) Remove(t *Target) {
	s.mu.Lock()
	delete(s.targets, t)
	t.removed = true
	s.mu.Unlock()
	d := s.d
	d.mu.Lock()
	d.targets.remove(t)
	d.mu.Unlock()
}

// wants reports whether the target takes the event of seq; sub.mu is held.
func (t *Target) wants(seq int64) bool { return !t.removed && seq > t.skip }

// Snapshot takes the target's result at the database's last commit, S, at or past the
// position it was added at (ADR-024 §5): each gets its documents, in the query's order (in
// path order for documents, those that exist). The events up to S are dropped for the target
// and those after it taken; Ready signals once the hub has released S. It fails, past
// SnapshotRows documents read, with a RESOURCE_EXHAUSTED *wire.Error.
func (t *Target) Snapshot(ctx context.Context, each func(core.Document) error) (Head, error) {
	s := t.sub
	seq, rt, err := s.d.reader.Head(ctx, s.db)
	if err != nil {
		return Head{}, err
	}
	at := Head{Seq: seq, ReadTime: rt}
	s.mu.Lock()
	t.skip = seq
	s.mu.Unlock()
	if err := t.ReadAt(ctx, at, each); err != nil {
		return Head{}, err
	}
	s.Await(seq)
	return at, nil
}
