package core

import (
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
