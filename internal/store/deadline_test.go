package store_test

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/arumadev/aruma/internal/platform/apierr"
	"github.com/arumadev/aruma/internal/store"
	"github.com/arumadev/aruma/internal/store/storetest"
)

// A frozen Postgres (paused, a stalled disk, a partition that keeps TCP open) answers nothing.
// With a timeout, every call on the pool gives up within it, with an error the APIs answer
// UNAVAILABLE: a statement in flight, a transaction's next statement, a caller waiting for a
// connection of a full pool, and a new connection. Once Postgres answers again the pool serves
// at once, with no goroutine or connection left behind.
func TestTimeoutBoundsCallsOnAFrozenDatabase(t *testing.T) {
	const bound = 500 * time.Millisecond
	ctx := context.Background()
	proxy, url := storetest.NewFreezeProxy(t, storetest.NewDB(t))
	pool, err := store.Open(ctx, url+"&pool_max_conns=2", store.Timeout(bound))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, "SELECT 1"); err != nil {
		t.Fatal(err)
	}
	pool.Reset() // no connection open yet: the freeze also meets a new connection
	time.Sleep(100 * time.Millisecond)
	baseGoroutines := runtime.NumGoroutine()

	// A transaction open before the freeze.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	proxy.Freeze()
	type result struct {
		what string
		err  error
		took time.Duration
	}
	results := make(chan result, 8)
	var wg sync.WaitGroup
	run := func(what string, f func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := time.Now()
			err := f()
			results <- result{what, err, time.Since(start)}
		}()
	}
	run("a statement of an open transaction", func() error {
		_, err := tx.Exec(ctx, "SELECT 1")
		return err
	})
	// The pool's second connection is opened against the frozen server; the others then wait
	// for a connection of the full pool.
	for _, what := range []string{"a statement on a new connection", "a wait for a connection", "another wait for a connection"} {
		run(what, func() error {
			return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
				_, err := tx.Exec(ctx, "SELECT 1")
				return err
			})
		})
	}
	wg.Wait()
	close(results)
	for r := range results {
		if r.err == nil {
			t.Errorf("%s succeeded on a frozen database", r.what)
			continue
		}
		if !apierr.IsUnavailable(r.err) {
			t.Errorf("%s: %v, which the APIs would not answer UNAVAILABLE", r.what, r.err)
		}
		// One bound for the statement, plus up to one for the connection it waited for.
		if r.took > 2*bound+time.Second {
			t.Errorf("%s gave up after %v, want about %v", r.what, r.took, bound)
		}
		t.Logf("%s: %v after %v", r.what, r.err, r.took.Round(time.Millisecond))
	}
	_ = tx.Rollback(ctx)

	// Still frozen: the pool has to open connections again, which time out on their own
	// (connect_timeout) as the wait for them does; either way the error is UNAVAILABLE.
	for i := range 3 {
		start := time.Now()
		_, err := pool.Exec(ctx, "SELECT 1")
		if err == nil || !apierr.IsUnavailable(err) || time.Since(start) > bound+time.Second {
			t.Errorf("call %d on a pool whose connections timed out: %v after %v, want UNAVAILABLE within %v", i, err, time.Since(start), bound)
		}
	}

	proxy.Thaw()
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, err := pool.Exec(ctx, "SELECT 1")
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the pool does not serve once the database answers: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	// What the freeze left: the connections that timed out are closed, their goroutines gone.
	// The baseline was taken with no connection open, and each connection the pool holds now
	// has the proxy's two goroutines: one or two connections, as the calls above left it.
	deadline = time.Now().Add(10 * time.Second)
	for {
		g, conns, open := runtime.NumGoroutine(), proxy.Conns(), pool.Stat().TotalConns()
		if g <= baseGoroutines+2*conns && int(open) == conns && conns <= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after the freeze: %d goroutines (%d before), %d connections through the proxy, %d in the pool", g, baseGoroutines, conns, open)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A caller's own deadline, shorter than the pool's timeout, still ends the call, and its error
// is the caller's (DEADLINE_EXCEEDED where an API maps it), not the database's timeout.
func TestTimeoutLeavesTheCallersDeadlineItsOwn(t *testing.T) {
	ctx := context.Background()
	proxy, url := storetest.NewFreezeProxy(t, storetest.NewDB(t))
	pool, err := store.Open(ctx, url, store.Timeout(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	proxy.Freeze()
	defer proxy.Thaw()
	cctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	_, err = pool.Exec(cctx, "SELECT 1")
	if err == nil || errors.Is(err, store.ErrTimeout) || apierr.IsUnavailable(err) {
		t.Fatalf("a call past the caller's own deadline: %v, want the caller's deadline, not the database's timeout", err)
	}
}

// A connection the pool opens to a frozen database times out on its own (connect_timeout,
// here shorter than the pool's timeout): that too is UNAVAILABLE, not the caller's deadline.
func TestConnectTimeoutOnAFrozenDatabaseIsUnavailable(t *testing.T) {
	ctx := context.Background()
	proxy, url := storetest.NewFreezeProxy(t, storetest.NewDB(t))
	pool, err := store.Open(ctx, url+"&connect_timeout=1", store.Timeout(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	pool.Reset()
	proxy.Freeze()
	defer proxy.Thaw()
	start := time.Now()
	_, err = pool.Exec(ctx, "SELECT 1")
	if err == nil || !apierr.IsUnavailable(err) || time.Since(start) > 3*time.Second {
		t.Fatalf("a call that needs a new connection: %v after %v, want UNAVAILABLE after about 1s", err, time.Since(start))
	}
}

// Background work marked Unbounded runs statements longer than the pool's timeout (a purge of
// a large project), where the same statement on a request's context is cut at the timeout. The
// wait for a connection stays bounded either way.
func TestUnboundedStatementsOutliveTheTimeout(t *testing.T) {
	const bound = 300 * time.Millisecond
	ctx := context.Background()
	pool, err := store.Open(ctx, storetest.NewDB(t)+"&pool_max_conns=1", store.Timeout(bound))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(store.Unbounded(ctx), "SELECT pg_sleep(1)"); err != nil {
		t.Errorf("an unbounded statement of 1s: %v", err)
	}
	if _, err := pool.Exec(ctx, "SELECT pg_sleep(1)"); !errors.Is(err, store.ErrTimeout) && !apierr.IsUnavailable(err) {
		t.Errorf("a bounded statement of 1s: %v, want the database timeout", err)
	}
	// The one connection is busy with an unbounded statement: waiting for it is bounded.
	busy := make(chan error, 1)
	go func() {
		_, err := pool.Exec(store.Unbounded(ctx), "SELECT pg_sleep(2)")
		busy <- err
	}()
	time.Sleep(200 * time.Millisecond)
	start := time.Now()
	_, err = pool.Exec(store.Unbounded(ctx), "SELECT 1")
	if !errors.Is(err, store.ErrTimeout) || time.Since(start) > bound+time.Second {
		t.Errorf("an unbounded call waiting for a connection: %v after %v, want the timeout", err, time.Since(start))
	}
	if err := <-busy; err != nil {
		t.Errorf("the unbounded statement holding the connection: %v", err)
	}
}
