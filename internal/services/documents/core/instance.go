package core

import (
	"cmp"
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
)

// Instance keeps the Document Database on one process per Postgres database (ADR-024 §7): the
// hub, the lock manager and the transactions live in memory, so two processes serving one
// database would break listeners and serializability silently. The process holds a
// session-level advisory lock on a dedicated connection, outside the pool; Postgres releases it
// when the session ends, so no lease needs renewing.
//
// The connection must reach Postgres itself: a pooler in transaction mode (pgbouncer
// pool_mode=transaction) does not keep a session's lock.
type Instance struct {
	cfg  *pgx.ConnConfig
	o    InstanceOptions
	held atomic.Bool

	stop context.CancelFunc
	done chan struct{}
	mu   sync.Mutex // guards conn
	conn *pgx.Conn
}

// InstanceOptions times the guard; the zero value is ADR-024 §7's.
type InstanceOptions struct {
	// BootWait is how long StartInstance tries before it returns without the lock: longer
	// than the previous process's drain, so a stop-then-start deploy takes over. Default 30s.
	BootWait time.Duration
	// Check is how often the held connection is checked. Default 1s.
	Check time.Duration
	// MinBackoff and MaxBackoff bound the retries while the lock is not held. Default 1s, 30s.
	MinBackoff, MaxBackoff time.Duration
	// OnAcquired runs when the lock is taken after StartInstance returned (a takeover, or a
	// recovery after a loss): the service starts a new hub and lock manager. OnLost runs when
	// it is lost: the service closes every stream, drops its open transactions, its hub and its
	// lock manager. Both run on the guard's goroutine.
	OnAcquired, OnLost func()
	Log                *slog.Logger
}

// instanceLock is the advisory lock's key, the int4 pair as Storage's are taken.
const instanceLock = "SELECT pg_try_advisory_lock(hashtext('documents.instance'), 0)"

// StartInstance tries to take the lock for up to BootWait, then keeps it, or keeps trying for it,
// in the background until Close. It returns whether the lock is held; when it is not, the
// process serves the rest of Aruma and the Document Database answers ErrNotServing.
func StartInstance(ctx context.Context, cfg *pgx.ConnConfig, o InstanceOptions) *Instance {
	o.BootWait = cmp.Or(o.BootWait, 30*time.Second)
	o.Check = cmp.Or(o.Check, time.Second)
	o.MinBackoff = cmp.Or(o.MinBackoff, time.Second)
	o.MaxBackoff = cmp.Or(o.MaxBackoff, 30*time.Second)
	if o.Log == nil {
		o.Log = slog.Default()
	}
	i := &Instance{cfg: cfg.Copy(), o: o, done: make(chan struct{})}
	ctx, i.stop = context.WithCancel(context.WithoutCancel(ctx))

	deadline := time.Now().Add(o.BootWait)
	for !i.tryAcquire(ctx) && time.Now().Before(deadline) {
		if !sleep(ctx, min(o.MinBackoff, time.Until(deadline))) {
			break
		}
	}
	if !i.held.Load() {
		o.Log.Error("documents: another Aruma instance serves the Document Database on this Postgres database; " +
			"the Document Database runs as a single instance and is unavailable here until that instance stops")
	}
	go i.run(ctx)
	return i
}

// Held reports whether this process holds the lock and may serve the Document Database.
func (i *Instance) Held() bool { return i.held.Load() }

// Close stops the guard and releases the lock.
func (i *Instance) Close() {
	i.stop()
	<-i.done
}

func (i *Instance) run(ctx context.Context) {
	defer close(i.done)
	defer i.release()
	backoff := i.o.MinBackoff
	for {
		if !i.held.Load() {
			if !sleep(ctx, backoff) {
				return
			}
			if !i.tryAcquire(ctx) {
				backoff = min(2*backoff, i.o.MaxBackoff)
				continue
			}
			backoff = i.o.MinBackoff
			i.o.Log.Info("documents: took the single-instance lock; serving the Document Database")
			if i.o.OnAcquired != nil {
				i.o.OnAcquired()
			}
			continue
		}
		if !sleep(ctx, i.o.Check) {
			return
		}
		if err := i.check(ctx); err != nil && ctx.Err() == nil {
			i.release()
			i.o.Log.Error("documents: lost the single-instance lock's connection; the Document Database is unavailable until it is taken again", "error", err)
			if i.o.OnLost != nil {
				i.o.OnLost()
			}
		}
	}
}

// tryAcquire connects and takes the lock if free, and reports whether it holds it.
func (i *Instance) tryAcquire(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, i.cfg)
	if err != nil {
		return false
	}
	var ok bool
	if err := conn.QueryRow(ctx, instanceLock).Scan(&ok); err != nil || !ok {
		_ = conn.Close(context.Background())
		return false
	}
	i.mu.Lock()
	i.conn = conn
	i.mu.Unlock()
	i.held.Store(true)
	return true
}

// check runs a statement on the lock's connection: an error is a lost session, and with it the
// lock.
func (i *Instance) check(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, max(i.o.Check, time.Second))
	defer cancel()
	i.mu.Lock()
	conn := i.conn
	i.mu.Unlock()
	_, err := conn.Exec(ctx, "SELECT 1")
	return err
}

// release stops serving, then closes the connection, which frees the lock.
func (i *Instance) release() {
	i.held.Store(false)
	i.mu.Lock()
	conn := i.conn
	i.conn = nil
	i.mu.Unlock()
	if conn != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = conn.Close(ctx)
	}
}

// sleep waits d, or reports false if ctx ends first.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
