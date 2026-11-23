package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultTimeout is how long the server waits for the database by default (database_timeout):
// for a connection from the pool, and then for each statement's answer. A frozen Postgres (a
// paused container, a stalled disk, a network partition that keeps TCP open) answers nothing
// and keeps its connections open, so without a bound every request waits for as long as the
// freeze lasts and the pool fills with them. 15 seconds is above every statement a live server
// runs on a request's path (the Document Database bounds a commit's statements at 10 seconds
// and its lock waits at 5), and well below the 30 seconds after which the Firebase SDKs give up
// on a request themselves, so a client is told UNAVAILABLE and retries instead of timing out.
const DefaultTimeout = 15 * time.Second

// ErrTimeout is the error of a database call that did not answer within the timeout: the
// database is unreachable as far as the request can tell. apierr.IsUnavailable recognises it
// (DatabaseTimeout), so every API answers it with UNAVAILABLE (503), which clients retry.
var ErrTimeout error = timeoutError{}

type timeoutError struct{}

func (timeoutError) Error() string         { return "the database did not answer in time" }
func (timeoutError) DatabaseTimeout() bool { return true }
func (timeoutError) Timeout() bool         { return true }

// cancelKey carries the cancel of the deadline a tracer call added, for its end.
type cancelKey struct{}

// timeoutCtx is a context whose own deadline, once passed, reports ErrTimeout as its error,
// so the error of the call it bounded says the database did not answer. A deadline the
// caller's context had already set reports as usual.
type timeoutCtx struct{ context.Context }

func (c timeoutCtx) Err() error {
	err := c.Context.Err()
	if err != nil && errors.Is(context.Cause(c.Context), ErrTimeout) {
		return ErrTimeout
	}
	return err
}

// deadlineTracer bounds every call on the pool's connections: the wait for a connection
// (pgxpool's Acquire) and each statement, batch and copy, until its result is read. A call
// past the bound fails with ErrTimeout and its connection is closed (pgx closes a connection
// whose context ends mid-call), so the pool replaces it when the database answers again.
type deadlineTracer struct{ d time.Duration }

func (t deadlineTracer) start(ctx context.Context) context.Context {
	bounded, cancel := context.WithDeadlineCause(ctx, time.Now().Add(t.d), ErrTimeout)
	return context.WithValue(timeoutCtx{bounded}, cancelKey{}, cancel)
}

func (deadlineTracer) end(ctx context.Context) {
	if cancel, ok := ctx.Value(cancelKey{}).(context.CancelFunc); ok {
		cancel()
	}
}

func (t deadlineTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	return t.start(ctx)
}

func (t deadlineTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	t.end(ctx)
}

func (t deadlineTracer) TraceBatchStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceBatchStartData) context.Context {
	return t.start(ctx)
}

func (deadlineTracer) TraceBatchQuery(context.Context, *pgx.Conn, pgx.TraceBatchQueryData) {}

func (t deadlineTracer) TraceBatchEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceBatchEndData) {
	t.end(ctx)
}

func (t deadlineTracer) TraceCopyFromStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceCopyFromStartData) context.Context {
	return t.start(ctx)
}

func (t deadlineTracer) TraceCopyFromEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceCopyFromEndData) {
	t.end(ctx)
}

func (t deadlineTracer) TracePrepareStart(ctx context.Context, _ *pgx.Conn, _ pgx.TracePrepareStartData) context.Context {
	return t.start(ctx)
}

func (t deadlineTracer) TracePrepareEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TracePrepareEndData) {
	t.end(ctx)
}

func (t deadlineTracer) TraceAcquireStart(ctx context.Context, _ *pgxpool.Pool, _ pgxpool.TraceAcquireStartData) context.Context {
	return t.start(ctx)
}

func (t deadlineTracer) TraceAcquireEnd(ctx context.Context, _ *pgxpool.Pool, _ pgxpool.TraceAcquireEndData) {
	t.end(ctx)
}

// connectionCheckInterval is how often Postgres checks, while it runs a statement, that the
// client is still connected (client_connection_check_interval): a call that timed out closes
// its connection, and Postgres then stops the statement within this interval instead of
// running it to its end for nobody.
const connectionCheckInterval = "5s"

// applyTimeout bounds the pool's calls by d (see deadlineTracer): new connections too, unless
// the URL sets connect_timeout, since a frozen server accepts TCP connections and never
// answers the startup.
func applyTimeout(cfg *pgxpool.Config, d time.Duration) {
	if d <= 0 {
		return
	}
	cfg.ConnConfig.Tracer = deadlineTracer{d: d}
	if cfg.ConnConfig.ConnectTimeout == 0 {
		cfg.ConnConfig.ConnectTimeout = d
	}
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		_, err := c.Exec(ctx, "SET client_connection_check_interval = '"+connectionCheckInterval+"'")
		return err
	}
}
