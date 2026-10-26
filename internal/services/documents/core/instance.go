package core

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgconn/ctxwatch"
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
	// CheckTimeout is how long a check may take before it is cancelled and counted as slow,
	// which is not a loss: the lock is lost only when its session is (ADR-024 §7). Default 10s.
	CheckTimeout time.Duration
	// MinBackoff and MaxBackoff bound the retries while the lock is not held. Default 1s, 30s.
	MinBackoff, MaxBackoff time.Duration
	// OnAcquired runs when the lock is taken after StartInstance returned (a takeover, or a
	// recovery after a loss): the service starts a new hub and lock manager. OnLost runs when
	// it is lost: the service closes every stream, drops its open transactions, its hub and its
	// lock manager. Both run on the guard's goroutine.
	OnAcquired, OnLost func()
	Log                *slog.Logger

	// checkSQL replaces instanceHeld in tests, to make a check slow.
	checkSQL string
}

// instanceLock is the advisory lock's key, the int4 pair as Storage's are taken.
const instanceLock = "SELECT pg_try_advisory_lock(hashtext('documents.instance'), 0)"

// instanceHeld reports whether this session still holds the lock: an int4-pair advisory lock
// is classid = the first key (as oid), objid = the second, objsubid = 2.
const instanceHeld = "SELECT " + instanceHeldCond

const instanceHeldCond = `EXISTS (SELECT 1 FROM pg_locks WHERE locktype = 'advisory' AND granted
	AND pid = pg_backend_pid() AND classid = hashtext('documents.instance')::oid AND objid = 0 AND objsubid = 2)`

// cancelGrace is how long a cancelled check may wait for Postgres to answer its cancel request
// before pgx closes the connection: a server that answers nothing for that long is taken for
// gone, and closing the connection does release the lock.
const cancelGrace = 30 * time.Second

// errLockGone is a check that found the session alive without the lock.
var errLockGone = errors.New("the session no longer holds the advisory lock")

// StartInstance tries to take the lock for up to BootWait, then keeps it, or keeps trying for it,
// in the background until Close. It returns whether the lock is held; when it is not, the
// process serves the rest of Aruma and the Document Database answers ErrNotServing.
func StartInstance(ctx context.Context, cfg *pgx.ConnConfig, o InstanceOptions) *Instance {
	o.BootWait = cmp.Or(o.BootWait, 30*time.Second)
	o.Check = cmp.Or(o.Check, time.Second)
	o.CheckTimeout = cmp.Or(o.CheckTimeout, 10*time.Second)
	o.checkSQL = cmp.Or(o.checkSQL, instanceHeld)
	o.MinBackoff = cmp.Or(o.MinBackoff, time.Second)
	o.MaxBackoff = cmp.Or(o.MaxBackoff, 30*time.Second)
	if o.Log == nil {
		o.Log = slog.Default()
	}
	i := &Instance{cfg: cfg.Copy(), o: o, done: make(chan struct{})}
	// A check that times out sends Postgres a cancel request, and keeps the connection: pgx's
	// default closes it, which would free the lock over a slow statement.
	i.cfg.BuildContextWatcherHandler = func(c *pgconn.PgConn) ctxwatch.Handler {
		return &pgconn.CancelRequestContextWatcherHandler{Conn: c, DeadlineDelay: cancelGrace}
	}
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
		lost, err := i.check(ctx)
		switch {
		case ctx.Err() != nil:
			return
		case lost:
			i.release()
			i.o.Log.Error("documents: lost the single-instance lock; the Document Database is unavailable until it is taken again", "error", err)
			if i.o.OnLost != nil {
				i.o.OnLost()
			}
		case err != nil:
			// Slow, or failed without ending the session: the lock is still this process's.
			// The next check comes after Check, as usual.
			i.o.Log.Warn("documents: the single-instance lock's check did not complete; the lock is kept", "error", err)
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

// check asks the lock's session whether it still holds the lock. It reports lost when the
// connection is gone, or when the session answers without the lock; any other error (a check
// slower than CheckTimeout, cancelled by Postgres) is reported with lost false, since a slow
// server has not released the lock (ADR-024 §7).
func (i *Instance) check(ctx context.Context) (lost bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, i.o.CheckTimeout)
	defer cancel()
	i.mu.Lock()
	conn := i.conn
	i.mu.Unlock()
	if conn == nil || conn.IsClosed() {
		return true, errors.New("the lock's connection is closed")
	}
	var held bool
	err = conn.QueryRow(ctx, i.o.checkSQL).Scan(&held)
	switch {
	case err == nil && !held:
		return true, errLockGone
	case err == nil:
		return false, nil
	case conn.IsClosed() || sessionEnded(err):
		return true, err
	}
	return false, err
}

// sessionEnded reports whether err is Postgres ending the session: a connection exception
// (class 08) or an operator intervention other than a cancelled statement (57P01 to 57P05).
func sessionEnded(err error) bool {
	pgErr, ok := errors.AsType[*pgconn.PgError](err)
	if !ok {
		return false
	}
	return strings.HasPrefix(pgErr.Code, "08") || strings.HasPrefix(pgErr.Code, "57P")
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
