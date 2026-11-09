package store

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

// InstanceLock keeps a service on one process per Postgres database when its state lives in
// memory (the Realtime Database's listeners, for one): two processes serving one database
// would store each other's writes and never tell their listeners. The process holds a
// session-level advisory lock on a dedicated connection, outside the pool. Postgres releases
// it when the session ends, so no lease needs renewing. This is the Document Database's
// lock (documents/core/instance.go) without its commit fence and barrier, which that service
// needs for transactions in flight.
//
// The connection must reach Postgres itself: a pooler in transaction mode (pgbouncer
// pool_mode=transaction) does not keep a session's lock.
type InstanceLock struct {
	cfg  *pgx.ConnConfig
	o    InstanceLockOptions
	held atomic.Bool

	stop context.CancelFunc
	done chan struct{}
	mu   sync.Mutex // guards conn
	conn *pgx.Conn
}

// InstanceLockOptions name and time the lock; the zero value is the Document Database's timing.
type InstanceLockOptions struct {
	// Name is the lock's key, hashed with hashtext into the first int4 of the advisory lock
	// pair, as the other services' locks are taken (for example "rtdb.instance").
	Name string
	// Service is how the log names the service (for example "rtdb").
	Service string
	// BootWait is how long StartInstanceLock tries before it returns without the lock: longer
	// than the previous process's drain, so a stop-then-start deploy takes over. Default 30s.
	BootWait time.Duration
	// Check is how often the held connection is checked. Default 1s.
	Check time.Duration
	// CheckTimeout is how long a check may take before it is cancelled and counted as slow,
	// which is not a loss: the lock is lost only when its session is. Default 10s.
	CheckTimeout time.Duration
	// MinBackoff and MaxBackoff bound the retries while the lock is not held. Default 1s, 30s.
	MinBackoff, MaxBackoff time.Duration
	// OnAcquired runs when the lock is taken after StartInstanceLock returned (a takeover, or
	// a recovery after a loss); OnLost runs when it is lost. Both run on the guard's goroutine.
	OnAcquired, OnLost func()
	Log                *slog.Logger
}

const (
	instanceLockTake = "SELECT pg_try_advisory_lock(hashtext($1), 0)"
	// An int4-pair advisory lock is classid = the first key (as oid), objid = the second,
	// objsubid = 2.
	instanceLockHeld = `SELECT EXISTS (SELECT 1 FROM pg_locks WHERE locktype = 'advisory' AND granted
		AND pid = pg_backend_pid() AND classid = hashtext($1)::oid AND objid = 0 AND objsubid = 2)`
	// instanceLockCancelGrace is how long a cancelled check may wait for Postgres to answer its
	// cancel request before pgx closes the connection: a server that answers nothing for that
	// long is taken for gone, and closing the connection does release the lock.
	instanceLockCancelGrace = 30 * time.Second
)

var errInstanceLockGone = errors.New("the session no longer holds the advisory lock")

// StartInstanceLock tries to take the lock for up to BootWait, then keeps it, or keeps trying
// for it, in the background until Close. Check Held for whether it is held: when it is not, the
// process serves the rest of Aruma and the service answers as unavailable.
func StartInstanceLock(ctx context.Context, cfg *pgx.ConnConfig, o InstanceLockOptions) *InstanceLock {
	o.BootWait = cmp.Or(o.BootWait, 30*time.Second)
	o.Check = cmp.Or(o.Check, time.Second)
	o.CheckTimeout = cmp.Or(o.CheckTimeout, 10*time.Second)
	o.MinBackoff = cmp.Or(o.MinBackoff, time.Second)
	o.MaxBackoff = cmp.Or(o.MaxBackoff, 30*time.Second)
	o.Service = cmp.Or(o.Service, o.Name)
	if o.Log == nil {
		o.Log = slog.Default()
	}
	l := &InstanceLock{cfg: cfg.Copy(), o: o, done: make(chan struct{})}
	// A check that times out sends Postgres a cancel request and keeps the connection: pgx's
	// default closes it, which would free the lock over a slow statement.
	l.cfg.BuildContextWatcherHandler = func(c *pgconn.PgConn) ctxwatch.Handler {
		return &pgconn.CancelRequestContextWatcherHandler{Conn: c, DeadlineDelay: instanceLockCancelGrace}
	}
	ctx, l.stop = context.WithCancel(context.WithoutCancel(ctx))

	deadline := time.Now().Add(o.BootWait)
	for !l.tryAcquire(ctx) && time.Now().Before(deadline) {
		if !instanceSleep(ctx, min(o.MinBackoff, time.Until(deadline))) {
			break
		}
	}
	if !l.held.Load() {
		o.Log.Error(o.Service + ": another Aruma instance serves " + o.Service + " on this Postgres database; " +
			"it runs as a single instance and is unavailable here until that instance stops")
	}
	go l.run(ctx)
	return l
}

// Held reports whether this process holds the lock and may serve.
func (l *InstanceLock) Held() bool { return l.held.Load() }

// Close stops the guard and releases the lock.
func (l *InstanceLock) Close() {
	l.stop()
	<-l.done
}

func (l *InstanceLock) run(ctx context.Context) {
	defer close(l.done)
	defer l.release()
	backoff := l.o.MinBackoff
	for {
		if !l.held.Load() {
			if !instanceSleep(ctx, backoff) {
				return
			}
			if !l.tryAcquire(ctx) {
				backoff = min(2*backoff, l.o.MaxBackoff)
				continue
			}
			backoff = l.o.MinBackoff
			l.o.Log.Info(l.o.Service + ": took the single-instance lock; serving")
			if l.o.OnAcquired != nil {
				l.o.OnAcquired()
			}
			continue
		}
		if !instanceSleep(ctx, l.o.Check) {
			return
		}
		lost, err := l.check(ctx)
		switch {
		case ctx.Err() != nil:
			return
		case lost:
			l.release()
			l.o.Log.Error(l.o.Service+": lost the single-instance lock; unavailable until it is taken again", "error", err)
			if l.o.OnLost != nil {
				l.o.OnLost()
			}
		case err != nil:
			// Slow, or failed without ending the session: the lock is still this process's.
			l.o.Log.Warn(l.o.Service+": the single-instance lock's check did not complete; the lock is kept", "error", err)
		}
	}
}

// tryAcquire connects and takes the lock if free, and reports whether it holds it.
func (l *InstanceLock) tryAcquire(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, l.cfg)
	if err != nil {
		return false
	}
	var ok bool
	if err := conn.QueryRow(ctx, instanceLockTake, l.o.Name).Scan(&ok); err != nil || !ok {
		_ = conn.Close(context.Background())
		return false
	}
	l.mu.Lock()
	l.conn = conn
	l.mu.Unlock()
	l.held.Store(true)
	return true
}

// check asks the lock's session whether it still holds the lock. It reports lost when the
// connection is gone or the session answers without the lock; any other error (a check slower
// than CheckTimeout) is reported with lost false, since a slow server has not released it.
func (l *InstanceLock) check(ctx context.Context) (lost bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, l.o.CheckTimeout)
	defer cancel()
	l.mu.Lock()
	conn := l.conn
	l.mu.Unlock()
	if conn == nil || conn.IsClosed() {
		return true, errors.New("the lock's connection is closed")
	}
	var held bool
	err = conn.QueryRow(ctx, instanceLockHeld, l.o.Name).Scan(&held)
	switch {
	case err == nil && !held:
		return true, errInstanceLockGone
	case err == nil:
		return false, nil
	case conn.IsClosed() || instanceSessionEnded(err):
		return true, err
	}
	return false, err
}

// instanceSessionEnded reports whether err is Postgres ending the session: a connection
// exception (class 08) or an operator intervention other than a cancelled statement (57P01 to
// 57P05).
func instanceSessionEnded(err error) bool {
	pgErr, ok := errors.AsType[*pgconn.PgError](err)
	return ok && (strings.HasPrefix(pgErr.Code, "08") || strings.HasPrefix(pgErr.Code, "57P"))
}

// release stops serving, then closes the connection, which frees the lock.
func (l *InstanceLock) release() {
	l.held.Store(false)
	l.mu.Lock()
	conn := l.conn
	l.conn = nil
	l.mu.Unlock()
	if conn != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = conn.Close(ctx)
	}
}

func instanceSleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
