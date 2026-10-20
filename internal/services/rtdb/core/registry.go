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
type registry struct {
	mu     sync.Mutex
	dbs    map[string]*Database
	closed bool

	store    Store
	now      func() time.Time
	log      *slog.Logger
	limits   Limits
	idle     time.Duration
	onCommit func(*Database, Commit)
}

func newRegistry(store Store, now func() time.Time, log *slog.Logger, limits Limits, idle time.Duration) *registry {
	return &registry{dbs: map[string]*Database{}, store: store, now: now, log: log, limits: limits, idle: idle}
}

// acquire returns the writer of a database, started if needed, and takes a reference on it.
func (r *registry) acquire(project int64, name string) (*Database, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, ErrUnavailable
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
	time.AfterFunc(r.idle, func() { r.retire(d) })
}

// retire stops an idle writer, unless a session took it up again meanwhile.
func (r *registry) retire(d *Database) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if d.refs == 0 && r.dbs[d.name] == d {
		r.stop(d)
	}
}

// stop closes the writer's inbox; it finishes what is queued and exits. Callers hold mu.
func (r *registry) stop(d *Database) {
	if r.dbs[d.name] == d {
		delete(r.dbs, d.name)
		close(d.inbox)
	}
}

// close stops every writer once its sessions are gone, and waits for them until ctx ends.
func (r *registry) close(ctx context.Context) error {
	r.mu.Lock()
	r.closed = true
	all := make([]*Database, 0, len(r.dbs))
	for _, d := range r.dbs {
		all = append(all, d)
		if d.refs == 0 {
			r.stop(d)
		}
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
