package core

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// registry maps namespaces to their writers. A writer starts with the first session on its
// database and stops once no session has used it for idleTimeout. Sessions hold references,
// and only they submit commands, so a writer's inbox closes only when nothing can send to it.
// One database never has two writers: a replacement starts only after its predecessor has
// finished what it had queued.
type registry struct {
	mu       sync.Mutex
	dbs      map[string]*Database
	retiring map[string]*Database // stopped, still draining their inbox
	closed   bool

	store    Store
	now      func() time.Time
	log      *slog.Logger
	limits   Limits
	idle     time.Duration
	onCommit func(*Database, Commit)
}

func newRegistry(store Store, now func() time.Time, log *slog.Logger, limits Limits, idle time.Duration) *registry {
	return &registry{
		dbs: map[string]*Database{}, retiring: map[string]*Database{},
		store: store, now: now, log: log, limits: limits, idle: idle,
	}
}

// acquire returns the writer of a database, started if needed, and takes a reference on it.
func (r *registry) acquire(project int64, name string) (*Database, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for {
		if r.closed {
			return nil, ErrUnavailable
		}
		old := r.retiring[name]
		if old == nil {
			break
		}
		// Wait for the predecessor to drain before starting its replacement.
		r.mu.Unlock()
		<-old.done
		r.mu.Lock()
	}
	d := r.dbs[name]
	if d == nil {
		d = newDatabase(project, name, r.store, r.now, r.log, r.limits, nil)
		if r.onCommit != nil {
			d.onCommit = func(c Commit) { r.onCommit(d, c) }
		}
		r.dbs[name] = d
		go d.run()
	}
	d.refs++
	// A retirement scheduled before this reference was taken no longer applies.
	d.gen++
	return d, nil
}

// release drops a reference. The last one schedules the writer's retirement.
func (r *registry) release(d *Database) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if d.refs--; d.refs > 0 {
		return
	}
	if r.closed {
		r.stop(d)
		return
	}
	gen := d.gen
	time.AfterFunc(r.idle, func() { r.retire(d, gen) })
}

// retire stops an idle writer, unless a session took it up again since the retirement was
// scheduled.
func (r *registry) retire(d *Database, gen uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if d.refs == 0 && d.gen == gen && r.dbs[d.name] == d {
		r.stop(d)
	}
}

// stop closes the writer's inbox; it finishes what is queued and exits. Callers hold mu.
func (r *registry) stop(d *Database) {
	if r.dbs[d.name] != d {
		return
	}
	delete(r.dbs, d.name)
	r.retiring[d.name] = d
	close(d.inbox)
	go func() {
		<-d.done
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.retiring[d.name] == d {
			delete(r.retiring, d.name)
		}
	}()
}

// close stops every writer once its sessions are gone, and waits for them until ctx ends.
func (r *registry) close(ctx context.Context) error {
	r.mu.Lock()
	r.closed = true
	all := make([]*Database, 0, len(r.dbs)+len(r.retiring))
	for _, d := range r.dbs {
		all = append(all, d)
		if d.refs == 0 {
			r.stop(d)
		}
	}
	for _, d := range r.retiring {
		all = append(all, d)
	}
	r.mu.Unlock()
	for _, d := range all {
		select {
		case <-d.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
