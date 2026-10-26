package core

import (
	"context"
	"errors"
	"math/rand/v2"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// clock is a test clock that moves only when told.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *clock { return &clock{now: time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newTable(t *testing.T, cfg TxnConfig) (*lockTable, *txnCounters) {
	t.Helper()
	cfg.defaults()
	stats := &txnCounters{}
	return newLockManager(&cfg, stats).table(testDB), stats
}

// queueAcquire runs acquire in the background and returns its outcome's channel, once the request is
// queued.
func queueAcquire(t *testing.T, tb *lockTable, lt *lockTxn, name string, mode lockMode) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- tb.acquire(context.Background(), lt, name, mode) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		tb.mu.Lock()
		queued := lt.waiting != nil || lt.done || lt.held[name] >= mode
		tb.mu.Unlock()
		if queued {
			return done
		}
		if time.Now().After(deadline) {
			t.Fatal("the request never queued")
		}
		time.Sleep(time.Millisecond)
	}
}

func outcome(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("the wait did not end")
		return nil
	}
}

func pending(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		t.Fatalf("the wait ended: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestLockModes(t *testing.T) {
	for _, c := range []struct {
		a, b, join lockMode
		compatible bool
	}{
		{lockShared, lockShared, lockShared, true},
		{lockIntent, lockIntent, lockIntent, true},
		{lockShared, lockIntent, lockExclusive, false},
		{lockIntent, lockShared, lockExclusive, false},
		{lockShared, lockExclusive, lockExclusive, false},
		{lockExclusive, lockShared, lockExclusive, false},
		{lockExclusive, lockExclusive, lockExclusive, false},
		{0, lockShared, lockShared, false},
	} {
		if got := join(c.a, c.b); got != c.join {
			t.Errorf("join(%d, %d) = %d, want %d", c.a, c.b, got, c.join)
		}
		if c.a != 0 && compatible(c.a, c.b) != c.compatible {
			t.Errorf("compatible(%d, %d) = %v", c.a, c.b, !c.compatible)
		}
	}
}

// f0-contention s3a: the older writer wounds the younger reader, and does not wait; the younger
// learns it on its next call.
func TestOlderWoundsYoungerHolder(t *testing.T) {
	tb, stats := newTable(t, TxnConfig{})
	ctx := t.Context()
	older, younger := tb.begin(0, false), tb.begin(0, false)
	if err := tb.acquire(ctx, older, "dE", lockShared); err != nil {
		t.Fatal(err)
	}
	if err := tb.acquire(ctx, younger, "dD", lockShared); err != nil {
		t.Fatal(err)
	}
	if err := tb.acquire(ctx, older, "dD", lockExclusive); err != nil {
		t.Fatalf("older writer: %v", err)
	}
	if err := tb.acquire(ctx, younger, "dD", lockShared); !errors.Is(err, ErrWounded) {
		t.Fatalf("younger's next call: %v", err)
	}
	if stats.wounds.Load() != 1 {
		t.Errorf("wounds %d", stats.wounds.Load())
	}
	tb.release(older)
	if n, locks := tb.open(); n != 0 || locks != 0 {
		t.Errorf("%d transactions, %d locks left", n, locks)
	}
}

// f0-contention s1: both read D; the younger's commit waits; the older's commit wounds it, at
// once, and goes through.
func TestOlderCommitWoundsWaitingYoungerCommit(t *testing.T) {
	tb, _ := newTable(t, TxnConfig{})
	ctx := t.Context()
	older, younger := tb.begin(0, false), tb.begin(0, false)
	for _, lt := range []*lockTxn{older, younger} {
		if err := tb.acquire(ctx, lt, "dD", lockShared); err != nil {
			t.Fatal(err)
		}
	}
	youngCommit := queueAcquire(t, tb, younger, "dD", lockExclusive)
	pending(t, youngCommit)
	if err := tb.acquire(ctx, older, "dD", lockExclusive); err != nil {
		t.Fatalf("older commit: %v", err)
	}
	if err := outcome(t, youngCommit); !errors.Is(err, ErrWounded) {
		t.Fatalf("younger commit: %v", err)
	}
}

// f0-contention s2: the younger waits for the older, which it does not wound, until the older
// ends.
func TestYoungerWaitsForOlder(t *testing.T) {
	tb, _ := newTable(t, TxnConfig{})
	ctx := t.Context()
	older, younger := tb.begin(0, false), tb.begin(0, false)
	for _, lt := range []*lockTxn{older, younger} {
		if err := tb.acquire(ctx, lt, "dD", lockShared); err != nil {
			t.Fatal(err)
		}
	}
	youngCommit := queueAcquire(t, tb, younger, "dD", lockExclusive)
	if err := tb.acquire(ctx, older, "dD", lockShared); err != nil {
		t.Fatalf("older re-read: %v", err)
	}
	pending(t, youngCommit)
	tb.release(older) // rollback
	if err := outcome(t, youngCommit); err != nil {
		t.Fatalf("younger commit after the rollback: %v", err)
	}
}

// f0-contention s3b: age is taken at the first lock request, not at begin. T1 began first but
// read nothing; T2 read D; T1's blind write waits for T2 instead of wounding it.
func TestAgeFromFirstLock(t *testing.T) {
	tb, _ := newTable(t, TxnConfig{})
	ctx := t.Context()
	t1 := tb.begin(0, false)
	t2 := tb.begin(0, false)
	if err := tb.acquire(ctx, t2, "dD", lockShared); err != nil {
		t.Fatal(err)
	}
	t1Commit := queueAcquire(t, tb, t1, "dD", lockExclusive)
	pending(t, t1Commit)
	if err := tb.acquire(ctx, t2, "dD", lockShared); err != nil {
		t.Fatalf("T2 re-read: %v", err)
	}
	if err := tb.acquire(ctx, t2, "dD", lockExclusive); err != nil {
		t.Fatalf("T2 commit: %v", err)
	}
	if err := tb.markCommitting(t2); err != nil {
		t.Fatal(err)
	}
	pending(t, t1Commit)
	tb.release(t2)
	if err := outcome(t, t1Commit); err != nil {
		t.Fatalf("T1 commit: %v", err)
	}
}

// f0-contention s6: a waiter gives up at the wait timeout with "Too much contention", and its
// transaction ends with its locks.
func TestWaitTimeout(t *testing.T) {
	tb, stats := newTable(t, TxnConfig{WaitTimeout: 50 * time.Millisecond})
	ctx := t.Context()
	reader, writer := tb.begin(0, false), tb.begin(0, true)
	if err := tb.acquire(ctx, reader, "dD", lockShared); err != nil {
		t.Fatal(err)
	}
	if err := tb.acquire(ctx, writer, "dE", lockExclusive); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err := tb.acquire(ctx, writer, "dD", lockExclusive)
	if !errors.Is(err, ErrContention) || time.Since(start) < 50*time.Millisecond {
		t.Fatalf("got %v after %v", err, time.Since(start))
	}
	if tb.state(writer) != ErrContention || stats.timeouts.Load() != 1 {
		t.Fatalf("writer state %v, timeouts %d", tb.state(writer), stats.timeouts.Load())
	}
	// E is free again.
	other := tb.begin(0, true)
	if err := tb.acquire(ctx, other, "dE", lockExclusive); err != nil {
		t.Fatal(err)
	}
}

// A cancelled wait withdraws its request and leaves the transaction open.
func TestCancelledWait(t *testing.T) {
	tb, _ := newTable(t, TxnConfig{})
	older, younger := tb.begin(0, false), tb.begin(0, false)
	if err := tb.acquire(t.Context(), older, "dD", lockExclusive); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- tb.acquire(ctx, younger, "dD", lockShared) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := outcome(t, done); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if tb.state(younger) != nil {
		t.Fatal("the transaction ended")
	}
	tb.release(older)
	if err := tb.acquire(t.Context(), younger, "dD", lockShared); err != nil {
		t.Fatal(err)
	}
}

// An older request wounds a younger one queued ahead of it that it conflicts with: otherwise
// the older would wait for the younger, and a cycle could last until the wait timeout.
func TestOlderWoundsYoungerQueuedAhead(t *testing.T) {
	tb, _ := newTable(t, TxnConfig{})
	ctx := t.Context()
	older := tb.begin(0, false)
	if err := tb.acquire(ctx, older, "dOther", lockShared); err != nil { // takes its age
		t.Fatal(err)
	}
	holder, writer := tb.begin(0, false), tb.begin(0, false)
	if err := tb.acquire(ctx, holder, "dD", lockShared); err != nil {
		t.Fatal(err)
	}
	if err := tb.acquire(ctx, writer, "dW", lockShared); err != nil {
		t.Fatal(err)
	}
	if holder.age > writer.age {
		t.Fatal("ages out of order")
	}
	// writer (younger than holder) waits for holder; older's shared request queues behind it.
	write := queueAcquire(t, tb, writer, "dD", lockExclusive)
	if err := tb.acquire(ctx, older, "dD", lockShared); err != nil {
		t.Fatalf("older: %v", err)
	}
	if err := outcome(t, write); !errors.Is(err, ErrWounded) {
		t.Fatalf("the younger writer queued ahead: %v", err)
	}
	if tb.state(holder) != nil {
		t.Fatal("the compatible holder was wounded")
	}
}

// An idle holder is never wounded: a younger writer waits out its wait timeout and gets the
// contention text, and the holder keeps its lock (f2-transactions.json lifetime
// idle_70s_then_plain_write; f0-contention s5).
func TestIdleHolderNotWounded(t *testing.T) {
	clk := newClock()
	tb, _ := newTable(t, TxnConfig{Now: clk.Now, WaitTimeout: 100 * time.Millisecond})
	ctx := t.Context()
	holder := tb.begin(0, false)
	if err := tb.acquire(ctx, holder, "dD", lockShared); err != nil {
		t.Fatal(err)
	}
	clk.advance(70 * time.Second)
	writer := tb.begin(0, true)
	if err := tb.acquire(ctx, writer, "dD", lockExclusive); !errors.Is(err, ErrContention) {
		t.Fatalf("writer: %v, want the contention text", err)
	}
	if err := tb.state(holder); err != nil {
		t.Fatalf("the idle holder ended: %v", err)
	}
	if err := tb.acquire(ctx, holder, "dD", lockExclusive); err != nil {
		t.Fatalf("the holder's upgrade after the writer gave up: %v", err)
	}
	tb.release(holder)
}

// A committing transaction is never wounded: the older one waits for it.
func TestCommittingNotWounded(t *testing.T) {
	tb, _ := newTable(t, TxnConfig{})
	ctx := t.Context()
	older := tb.begin(0, false)
	if err := tb.acquire(ctx, older, "dOther", lockShared); err != nil {
		t.Fatal(err)
	}
	younger := tb.begin(0, true)
	if err := tb.acquire(ctx, younger, "dD", lockExclusive); err != nil {
		t.Fatal(err)
	}
	if err := tb.markCommitting(younger); err != nil {
		t.Fatal(err)
	}
	read := queueAcquire(t, tb, older, "dD", lockShared)
	pending(t, read)
	tb.release(younger)
	if err := outcome(t, read); err != nil {
		t.Fatal(err)
	}
}

// Requests are granted in arrival order: a shared request waits behind a queued exclusive one.
func TestFIFO(t *testing.T) {
	tb, _ := newTable(t, TxnConfig{})
	ctx := t.Context()
	a, b, c := tb.begin(0, false), tb.begin(0, false), tb.begin(0, false)
	if err := tb.acquire(ctx, a, "dD", lockShared); err != nil {
		t.Fatal(err)
	}
	write := queueAcquire(t, tb, b, "dD", lockExclusive)
	read := queueAcquire(t, tb, c, "dD", lockShared)
	pending(t, read)
	tb.release(a)
	if err := outcome(t, write); err != nil {
		t.Fatal(err)
	}
	pending(t, read)
	tb.release(b)
	if err := outcome(t, read); err != nil {
		t.Fatal(err)
	}
}

// Scopes: a listing's shared lock and a write's intent lock conflict; writes' intents do not.
func TestScopeLocks(t *testing.T) {
	tb, _ := newTable(t, TxnConfig{WaitTimeout: 30 * time.Millisecond})
	ctx := t.Context()
	w1, w2 := tb.begin(0, true), tb.begin(0, true)
	if err := tb.acquire(ctx, w1, "cX", lockIntent); err != nil {
		t.Fatal(err)
	}
	if err := tb.acquire(ctx, w2, "cX", lockIntent); err != nil {
		t.Fatalf("intent with intent: %v", err)
	}
	tb.release(w1)
	tb.release(w2)
	lister := tb.begin(0, false)
	if err := tb.acquire(ctx, lister, "cX", lockShared); err != nil {
		t.Fatal(err)
	}
	writer := tb.begin(0, true)
	if err := tb.acquire(ctx, writer, "cX", lockIntent); !errors.Is(err, ErrContention) {
		t.Fatalf("intent under a listing: %v", err)
	}
	// The lister writing into its own scope holds shared and intent: exclusive.
	if err := tb.acquire(ctx, lister, "cX", lockIntent); err != nil || lister.held["cX"] != lockExclusive {
		t.Fatalf("shared then intent: %v, %d", err, lister.held["cX"])
	}
}

func TestMaxLocks(t *testing.T) {
	tb, _ := newTable(t, TxnConfig{MaxLocks: 2})
	ctx := t.Context()
	lt := tb.begin(0, false)
	for i := range 2 {
		if err := tb.acquire(ctx, lt, "d"+strconv.Itoa(i), lockShared); err != nil {
			t.Fatal(err)
		}
	}
	if err := tb.acquire(ctx, lt, "d0", lockExclusive); err != nil {
		t.Fatalf("an upgrade is no new lock: %v", err)
	}
	if err := tb.acquire(ctx, lt, "d2", lockShared); !errors.Is(err, errTooManyLocks) {
		t.Fatal(err)
	}
	// A commit's own locks are not bounded.
	c := tb.begin(0, true)
	for i := range 5 {
		if err := tb.acquire(ctx, c, "e"+strconv.Itoa(i), lockExclusive); err != nil {
			t.Fatal(err)
		}
	}
}

// lockInvariant checks the table under its mutex: an exclusive holder is alone, holders and
// their transactions agree, and every queued request is its transaction's wait.
func lockInvariant(tb *lockTable) error {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	for name, ls := range tb.locks {
		for h, m := range ls.holders {
			if m == lockExclusive && len(ls.holders) > 1 {
				return errors.New(name + ": an exclusive holder is not alone")
			}
			if h.held[name] != m || h.done {
				return errors.New(name + ": a holder disagrees")
			}
		}
		for _, r := range ls.queue {
			if r.txn.waiting != r {
				return errors.New(name + ": a queued request is not its transaction's wait")
			}
		}
	}
	return nil
}

// Random schedules of transactions over a few hot names: wound-wait leaves no wait to the
// timeout (no deadlock), the table's invariant holds throughout, every lock is freed at the end,
// and retried transactions, which keep their age, all finish. Each transaction reads the
// versions of the names it locks and bumps those it holds exclusive once committing: a version
// that moved under a lock is a broken lock.
func TestLockManagerRandomSchedules(t *testing.T) {
	tb, stats := newTable(t, TxnConfig{WaitTimeout: 10 * time.Second})
	const workers, each, names = 12, 150, 5
	var versions [names]atomic.Int64
	var committed atomic.Int64
	stop := make(chan struct{})
	var checker sync.WaitGroup
	checker.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			if err := lockInvariant(tb); err != nil {
				t.Error(err)
				return
			}
			time.Sleep(100 * time.Microsecond)
		}
	})
	var wg sync.WaitGroup
	for w := range workers {
		wg.Go(func() {
			rng := rand.New(rand.NewPCG(uint64(w), 7))
			ctx := context.Background()
			for range each {
				var age uint64
				for {
					lt := tb.begin(age, false)
					seen := map[int]int64{}
					err := func() error {
						for range 1 + rng.IntN(3) {
							n := rng.IntN(names)
							mode := lockShared
							if rng.IntN(3) == 0 {
								mode = lockExclusive
							}
							if err := tb.acquire(ctx, lt, "d"+strconv.Itoa(n), mode); err != nil {
								return err
							}
							if _, ok := seen[n]; !ok {
								seen[n] = versions[n].Load()
							}
						}
						return tb.markCommitting(lt)
					}()
					if err == nil {
						for n, v := range seen {
							if versions[n].Load() != v {
								t.Errorf("name %d changed under a lock", n)
							}
							if lt.held["d"+strconv.Itoa(n)] == lockExclusive {
								versions[n].Add(1)
							}
						}
						committed.Add(1)
					}
					age = tb.age(lt)
					tb.release(lt)
					if err == nil {
						break
					}
					if !errors.Is(err, ErrWounded) {
						t.Errorf("unexpected: %v", err)
						return
					}
				}
			}
		})
	}
	wg.Wait()
	close(stop)
	checker.Wait()
	if committed.Load() != workers*each || stats.timeouts.Load() != 0 {
		t.Fatalf("committed %d, timeouts %d", committed.Load(), stats.timeouts.Load())
	}
	if n, locks := tb.open(); n != 0 || locks != 0 {
		t.Fatalf("%d transactions, %d locks left", n, locks)
	}
	t.Logf("waits %d, wounds %d", stats.waits.Load(), stats.wounds.Load())
}
