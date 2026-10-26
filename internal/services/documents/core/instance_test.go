package core

import (
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/arumadev/aruma/internal/store"
	"github.com/arumadev/aruma/internal/store/storetest"
)

// waitFor polls cond until it holds or d passes.
func waitFor(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// One process holds the Document Database; a second is refused, takes over when the first's
// session is lost, and the first recovers when the second stops (ADR-024 §7).
func TestInstanceLock(t *testing.T) {
	url := storetest.NewDB(t)
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := store.Open(t.Context(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)

	var lost, acquired atomic.Int32
	opts := InstanceOptions{BootWait: 2 * time.Second, Check: 50 * time.Millisecond, MinBackoff: 50 * time.Millisecond,
		MaxBackoff: 200 * time.Millisecond, Log: slog.New(slog.DiscardHandler)}
	firstOpts := opts
	firstOpts.OnLost = func() { lost.Add(1) }
	firstOpts.OnAcquired = func() { acquired.Add(1) }
	first := StartInstance(t.Context(), cfg, firstOpts)
	t.Cleanup(first.Close)
	if !first.Held() {
		t.Fatal("the first instance does not hold the lock")
	}

	secondOpts := opts
	secondOpts.BootWait = 300 * time.Millisecond
	start := time.Now()
	second := StartInstance(t.Context(), cfg, secondOpts)
	t.Cleanup(second.Close)
	if second.Held() || time.Since(start) < 300*time.Millisecond {
		t.Fatal("a second instance took the lock, or gave up before BootWait")
	}
	s := NewPGStore(admin, Options{Instance: second})
	_, err = s.Commit(t.Context(), testDB, nil)
	wantError(t, err, Unavailable, ErrNotServing.Message)

	// The first's session ends under it: it stops serving, and the lock is taken again by one.
	var holders int
	if err := admin.QueryRow(t.Context(), `SELECT count(pg_terminate_backend(pid)) FROM pg_locks
		WHERE locktype = 'advisory' AND granted AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`).Scan(&holders); err != nil || holders != 1 {
		t.Fatalf("terminated %d holders: %v", holders, err)
	}
	waitFor(t, 3*time.Second, "the first instance to see its loss", func() bool { return lost.Load() == 1 })
	waitFor(t, 3*time.Second, "an instance to take the lock again", func() bool { return first.Held() || second.Held() })
	time.Sleep(300 * time.Millisecond) // both have retried by now
	if first.Held() == second.Held() {
		t.Fatalf("held by first %v, second %v", first.Held(), second.Held())
	}

	// Whichever holds it stops; the other takes it.
	holder, other := first, second
	if second.Held() {
		holder, other = second, first
	}
	holder.Close()
	waitFor(t, 3*time.Second, "the other instance to take over", other.Held)
	if other == first && acquired.Load() == 0 {
		t.Error("OnAcquired was not called on recovery")
	}
	if _, err := NewPGStore(admin, Options{Instance: other}).Commit(t.Context(), testDB, nil); err != nil {
		t.Fatalf("serving instance: %v", err)
	}
}

// A check slower than CheckTimeout is cancelled by Postgres, not by closing the connection: the
// session keeps the lock, the instance keeps serving, and no other process can take it
// (review M6).
func TestInstanceSlowCheckKeepsTheLock(t *testing.T) {
	url := storetest.NewDB(t)
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := store.Open(t.Context(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)

	var lost atomic.Int32
	i := StartInstance(t.Context(), cfg, InstanceOptions{
		BootWait: 2 * time.Second, Check: 20 * time.Millisecond, CheckTimeout: 50 * time.Millisecond,
		OnLost: func() { lost.Add(1) }, Log: slog.New(slog.DiscardHandler),
		checkSQL: "SELECT pg_sleep(0.5) IS NOT NULL AND " + instanceHeldCond,
	})
	t.Cleanup(i.Close)
	if !i.Held() {
		t.Fatal("the instance does not hold the lock")
	}
	time.Sleep(time.Second) // several checks time out
	if lost.Load() != 0 || !i.Held() {
		t.Fatalf("slow checks lost the lock: OnLost %d, held %v", lost.Load(), i.Held())
	}
	var free bool
	if err := admin.QueryRow(t.Context(), instanceLock).Scan(&free); err != nil || free {
		t.Fatalf("another session took the lock (%v): %v", free, err)
	}
}

// A check that finds its session alive without the lock reports the loss, and one that finds
// the connection closed does too.
func TestInstanceCheckSeesTheLockGone(t *testing.T) {
	url := storetest.NewDB(t)
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	// Check is long, so the guard's goroutine leaves the connection to the test.
	i := StartInstance(t.Context(), cfg, InstanceOptions{BootWait: 2 * time.Second, Check: time.Hour,
		Log: slog.New(slog.DiscardHandler)})
	t.Cleanup(i.Close)
	if lost, err := i.check(t.Context()); lost || err != nil {
		t.Fatalf("check while held = %v, %v", lost, err)
	}
	i.mu.Lock()
	conn := i.conn
	i.mu.Unlock()
	if _, err := conn.Exec(t.Context(), "SELECT pg_advisory_unlock(hashtext('documents.instance'), 0)"); err != nil {
		t.Fatal(err)
	}
	if lost, err := i.check(t.Context()); !lost || !errors.Is(err, errLockGone) {
		t.Fatalf("check after unlock = %v, %v", lost, err)
	}
	if err := conn.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if lost, _ := i.check(t.Context()); !lost {
		t.Fatal("check on a closed connection did not report the loss")
	}
}
