package listen

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/arumadev/aruma/internal/services/documents/core"
)

// Options configures a Hub; a zero value is its default.
type Options struct {
	// GapTimeout is how long a commit ahead of its turn waits for those before it to be told
	// of, before the hub reads them from the changelog (gap_timeout, ADR-024 §4). Default 10 ms.
	GapTimeout time.Duration
	// HoldLimit is the commits held ahead of their turn past which the hub reads the changelog
	// at once. Default 4096.
	HoldLimit int
	// LagEvents is the events a subscriber may leave untaken (documents.limits.stream_lag_events,
	// ADR-026 §3); past it the subscriber must resync. Default 4096.
	LagEvents int
	// SnapshotRows bounds the documents a target's read goes through
	// (documents.limits.snapshot_rows). Default 1,000,000.
	SnapshotRows int
	// UnknownPoll, UnknownFast and UnknownSlowPoll settle a commit whose outcome is unknown: the
	// changelog is read every UnknownPoll (20 ms) for UnknownFast (10 s), then every
	// UnknownSlowPoll (1 s) until it is settled.
	UnknownPoll, UnknownFast, UnknownSlowPoll time.Duration
	// FillCommits bounds the commits one changelog read returns. Default 256.
	FillCommits int
	Log         *slog.Logger
}

func (o Options) withDefaults() Options {
	o.GapTimeout = cmp.Or(o.GapTimeout, 10*time.Millisecond)
	o.HoldLimit = cmp.Or(o.HoldLimit, 4096)
	o.LagEvents = cmp.Or(o.LagEvents, 4096)
	o.SnapshotRows = cmp.Or(o.SnapshotRows, 1_000_000)
	o.UnknownPoll = cmp.Or(o.UnknownPoll, 20*time.Millisecond)
	o.UnknownFast = cmp.Or(o.UnknownFast, 10*time.Second)
	o.UnknownSlowPoll = cmp.Or(o.UnknownSlowPoll, time.Second)
	o.FillCommits = cmp.Or(o.FillCommits, 256)
	o.Log = cmp.Or(o.Log, slog.Default())
	return o
}

// ErrClosed is a subscription to a hub that is closed.
var ErrClosed = errors.New("listen: the hub is closed")

// Hub is the in-process hub (ADR-026 §2): core.Notifier for the store, and the subscribers'
// source of commits, per database in seq order. Close it when done.
type Hub struct {
	o      Options
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// mu guards the databases with subscribers, and their subscribers. The locks nest as
	// database.mu, then Hub.mu, then Subscriber.mu; none is taken under a later one.
	mu     sync.Mutex
	reader Reader
	dbs    map[dbKey]*database
	closed bool
}

type dbKey struct {
	project int64
	id      string
}

func keyOf(db core.DB) dbKey { return dbKey{db.Project, db.ID} }

var _ core.Notifier = (*Hub)(nil)

// NewHub returns a hub; Bind gives it its reader before anyone subscribes.
func NewHub(o Options) *Hub {
	ctx, cancel := context.WithCancel(context.Background())
	return &Hub{o: o.withDefaults(), ctx: ctx, cancel: cancel, dbs: map[dbKey]*database{}}
}

// Bind sets the reader: the store that tells the hub of its commits.
func (h *Hub) Bind(r Reader) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reader = r
}

// Close ends every subscription and waits for the hub's goroutines.
func (h *Hub) Close() {
	h.mu.Lock()
	h.closed = true
	var subs []*Subscriber
	for _, d := range h.dbs {
		for s := range d.subs {
			subs = append(subs, s)
		}
	}
	h.dbs = map[dbKey]*database{}
	h.mu.Unlock()
	h.cancel()
	for _, s := range subs {
		s.end()
	}
	h.wg.Wait()
}

// lookup returns the database's state, when it has subscribers.
func (h *Hub) lookup(db core.DB) *database {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.dbs[keyOf(db)]
}

// Committed implements core.Notifier.
func (h *Hub) Committed(db core.DB, seq int64, commitTime time.Time, changes []core.Change) {
	if d := h.lookup(db); d != nil {
		d.mu.Lock()
		d.offer(&commit{seq: seq, time: commitTime, changes: changes})
		d.mu.Unlock()
	}
}

// Unknown implements core.Notifier: the commit is settled from the changelog (ADR-024 §4).
func (h *Hub) Unknown(db core.DB, seq int64) {
	if d := h.lookup(db); d != nil {
		d.mu.Lock()
		d.unknown(seq)
		d.mu.Unlock()
	}
}

// Subscribe returns a subscriber to db's commits, from the last one released: the stream's
// side of the hub. Close it when done.
func (h *Hub) Subscribe(ctx context.Context, db core.DB) (*Subscriber, error) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil, ErrClosed
	}
	if h.reader == nil {
		h.mu.Unlock()
		return nil, errors.New("listen: the hub has no reader")
	}
	d := h.dbs[keyOf(db)]
	if d == nil {
		d = h.newDatabase(db)
	}
	s := newSubscriber(h, d, db)
	d.subs[s] = struct{}{}
	h.mu.Unlock()
	select {
	case <-d.ready:
	case <-ctx.Done():
		s.Close()
		return nil, ctx.Err()
	}
	if d.err != nil {
		s.Close()
		return nil, d.err
	}
	return s, nil
}

// leave removes s from its database, and the database once it has no subscriber.
func (h *Hub) leave(s *Subscriber) {
	h.mu.Lock()
	d := s.d
	delete(d.subs, s)
	if len(d.subs) == 0 && h.dbs[d.key] == d {
		delete(h.dbs, d.key)
		d.cancel()
	}
	h.mu.Unlock()
}

// commit is a commit as the hub releases it.
type commit struct {
	seq     int64
	time    time.Time
	changes []core.Change
}

// database is the hub's state of one database with subscribers: the release order (ADR-024
// §4) and the targets of its subscribers, indexed by where their documents live.
type database struct {
	h      *Hub
	key    dbKey
	db     core.DB
	reader Reader
	cancel context.CancelFunc
	// ready closes once the hub knows the last commit, or failed to (err).
	ready chan struct{}
	err   error
	// wake asks the filler to look again.
	wake chan struct{}
	// pos is the last commit released.
	pos atomic.Pointer[Head]

	// subs is guarded by Hub.mu.
	subs map[*Subscriber]struct{}

	mu        sync.Mutex
	known     bool
	next      int64
	held      map[int64]*commit
	heldSince time.Time
	// want is a seq whose outcome is unknown, to settle; wantHead asks the next fill to settle
	// the commit after the head it reads (Unknown with seq 0).
	want      int64
	wantHead  bool
	wantSince time.Time
	lastFill  time.Time
	targets   index
	// waiting maps a subscriber to the seq it is to be woken at (Subscriber.Await).
	waiting map[*Subscriber]int64
}

// newDatabase registers db's state and starts its filler, which first reads the last commit.
// Commits told of until then are held. h.mu is held.
func (h *Hub) newDatabase(db core.DB) *database {
	ctx, cancel := context.WithCancel(h.ctx)
	d := &database{
		h: h, key: keyOf(db), db: db, reader: h.reader, cancel: cancel,
		ready: make(chan struct{}), wake: make(chan struct{}, 1),
		subs: map[*Subscriber]struct{}{}, held: map[int64]*commit{}, waiting: map[*Subscriber]int64{},
		targets: newIndex(),
	}
	h.dbs[d.key] = d
	h.wg.Go(func() { d.run(ctx) })
	return d
}

func (d *database) signal() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// start reads the last commit: the release order starts past it.
func (d *database) start(ctx context.Context) bool {
	seq, t, err := d.reader.Head(ctx, d.db)
	if err != nil {
		d.err = err
		close(d.ready)
		d.h.mu.Lock()
		if d.h.dbs[d.key] == d {
			delete(d.h.dbs, d.key)
		}
		d.h.mu.Unlock()
		return false
	}
	d.mu.Lock()
	d.known, d.next = true, seq+1
	d.pos.Store(&Head{Seq: seq, ReadTime: t})
	for s := range d.held {
		if s <= seq {
			delete(d.held, s)
		}
	}
	d.drain()
	d.mu.Unlock()
	close(d.ready)
	return true
}

// offer takes a commit told of, or read from the changelog, and releases what is in turn.
func (d *database) offer(c *commit) {
	if d.known && c.seq < d.next {
		return // released already
	}
	d.held[c.seq] = c
	if d.known {
		d.drain()
	}
}

// drain releases the held commits from next on, consecutive, and asks the filler for a gap.
func (d *database) drain() {
	for {
		c := d.held[d.next]
		if c == nil {
			break
		}
		delete(d.held, d.next)
		d.release(c)
		d.next++
	}
	switch {
	case len(d.held) == 0:
		d.heldSince = time.Time{}
	case d.heldSince.IsZero():
		d.heldSince = time.Now()
		d.signal()
	case len(d.held) >= d.h.o.HoldLimit:
		d.signal()
	}
	if d.want > 0 && d.want < d.next {
		d.want = 0
	}
}

// unknown asks for a commit whose outcome is unknown to be settled from the changelog.
func (d *database) unknown(seq int64) {
	if d.known && seq > 0 && seq < d.next {
		return
	}
	if !d.wantHead && d.want == 0 {
		d.wantSince = time.Now()
	}
	if seq == 0 {
		d.wantHead = true
	} else {
		d.want = max(d.want, seq)
	}
	d.signal()
}

// release sends a commit to the subscribers whose targets it changes, then makes it the
// position: a subscriber that reads the position first has every event up to it queued.
func (d *database) release(c *commit) {
	var per map[*Subscriber][]TargetChange
	for _, ch := range c.changes {
		for t := range d.targets.candidates(ch.Path) {
			k := t.classify(ch.Before, ch.After)
			if k == 0 {
				continue
			}
			if per == nil {
				per = map[*Subscriber][]TargetChange{}
			}
			per[t.sub] = append(per[t.sub], TargetChange{Target: t, Kind: k, Before: ch.Before, After: ch.After})
		}
	}
	for s, changes := range per {
		s.push(Event{Seq: c.seq, CommitTime: c.time, Changes: changes})
	}
	d.pos.Store(&Head{Seq: c.seq, ReadTime: c.time})
	for s, at := range d.waiting {
		if at <= c.seq {
			delete(d.waiting, s)
			s.signal()
		}
	}
}

// resync jumps the release order past head, when the changelog no longer holds the commits
// before it: every subscriber must take its targets' results again (ADR-026 §1.4, case 2).
func (d *database) resync(head int64, t time.Time) {
	d.h.o.Log.Warn("documents: the changelog no longer holds commits the hub missed; its subscribers resync",
		"project", d.db.Project, "database", d.db.ID, "from", d.next, "head", head)
	// The subscribers are marked before the position moves: a Take in between returns the resync
	// with the old position, which is harmless. The other order would return the head without the
	// resync: a global snapshot missing the commits lost.
	d.h.mu.Lock()
	for s := range d.subs {
		s.markResync()
	}
	d.h.mu.Unlock()
	for s := range d.held {
		if s <= head {
			delete(d.held, s)
		}
	}
	d.next = head + 1
	d.pos.Store(&Head{Seq: head, ReadTime: t})
	for s := range d.waiting {
		delete(d.waiting, s)
		s.signal()
	}
	d.drain()
}

// run is the database's filler: it reads the changelog for a gap that outlasts GapTimeout, for
// HoldLimit commits held, and for a commit whose outcome is unknown, until settled.
func (d *database) run(ctx context.Context) {
	if !d.start(ctx) {
		return
	}
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		delay, fill := d.plan(time.Now())
		if fill {
			d.fill(ctx)
			continue
		}
		var tick <-chan time.Time
		if delay > 0 {
			if timer == nil {
				timer = time.NewTimer(delay)
			} else {
				timer.Reset(delay)
			}
			tick = timer.C
		}
		select {
		case <-ctx.Done():
			return
		case <-d.wake:
		case <-tick:
		}
		if timer != nil {
			timer.Stop()
		}
	}
}

// plan says whether to read the changelog now, else how long to wait (0: until woken).
func (d *database) plan(now time.Time) (time.Duration, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	o := d.h.o
	var due time.Time
	if d.wantHead || d.want > 0 {
		interval := o.UnknownPoll
		if now.Sub(d.wantSince) > o.UnknownFast {
			interval = o.UnknownSlowPoll
		}
		due = d.lastFill.Add(interval)
		if d.lastFill.Before(d.wantSince) {
			due = now // a first read at once
		}
	}
	if len(d.held) > 0 {
		gap := d.heldSince.Add(o.GapTimeout)
		if len(d.held) >= o.HoldLimit {
			gap = now
		}
		if due.IsZero() || gap.Before(due) {
			due = gap
		}
	}
	if due.IsZero() {
		return 0, false
	}
	// Reads are spaced, a failing one included.
	if spaced := d.lastFill.Add(min(o.GapTimeout, o.UnknownPoll)); due.Before(spaced) {
		due = spaced
	}
	if !due.After(now) {
		return 0, true
	}
	return due.Sub(now), false
}

// fill reads the commits after the last released from the changelog and releases them.
func (d *database) fill(ctx context.Context) {
	d.mu.Lock()
	after := d.next - 1
	d.lastFill = time.Now()
	d.mu.Unlock()
	head, headTime, commits, err := d.reader.LoggedCommits(ctx, d.db, after, d.h.o.FillCommits)
	if err != nil {
		if ctx.Err() == nil {
			d.h.o.Log.Warn("documents: the hub could not read the changelog", "project", d.db.Project, "database", d.db.ID, "err", err)
		}
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.wantHead {
		// The commit whose stamp's reply was lost is past head if it is still to commit: no
		// other can stamp while it holds the sequencer row.
		d.wantHead = false
		d.want = max(d.want, head+1)
	}
	// The first seq the changelog lacks, where it lacks one the hub has not released.
	missing := head + 1
	if len(commits) > 0 {
		missing = commits[0].Seq
	}
	if missing > after+1 && d.next < missing && d.next <= head {
		d.resync(head, headTime)
		return
	}
	for _, c := range commits {
		d.offer(&commit{seq: c.Seq, time: c.CommitTime, changes: c.Changes})
	}
	if d.want > 0 && d.want < d.next {
		d.want = 0
	}
	if len(commits) > 0 && commits[len(commits)-1].Seq < head {
		d.signal() // more to read
		d.lastFill = time.Time{}
	}
}
