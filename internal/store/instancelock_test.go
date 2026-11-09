package store_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/arumadev/aruma/internal/store"
	"github.com/arumadev/aruma/internal/store/storetest"
)

func fast(o store.InstanceLockOptions) store.InstanceLockOptions {
	o.Name, o.Service = "test.instance", "test"
	o.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	o.Check, o.MinBackoff, o.MaxBackoff = 20*time.Millisecond, 20*time.Millisecond, 50*time.Millisecond
	return o
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// A second process on the same database does not get the lock while the first holds it, takes
// it when the first stops, and the first, if it loses its Postgres session, takes it again.
func TestInstanceLockIsExclusiveAndRegained(t *testing.T) {
	ctx := context.Background()
	url := storetest.NewDB(t)
	pool, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	cfg := pool.Config().ConnConfig

	lost, regained := make(chan struct{}, 4), make(chan struct{}, 4)
	first := store.StartInstanceLock(ctx, cfg, fast(store.InstanceLockOptions{
		BootWait:   time.Second,
		OnLost:     func() { lost <- struct{}{} },
		OnAcquired: func() { regained <- struct{}{} },
	}))
	defer first.Close()
	if !first.Held() {
		t.Fatal("the first instance did not get a free lock")
	}

	second := store.StartInstanceLock(ctx, cfg, fast(store.InstanceLockOptions{BootWait: 100 * time.Millisecond}))
	defer second.Close()
	if second.Held() {
		t.Fatal("the second instance holds the lock while the first does")
	}
	time.Sleep(200 * time.Millisecond)
	if second.Held() {
		t.Fatal("the second instance took the lock from the first")
	}

	// The first loses its session (a Postgres restart, a failover): it notices, and one of the
	// two takes the lock again; never both.
	kill := func() {
		var n int
		err := pool.QueryRow(ctx, `SELECT count(pg_terminate_backend(pid)) FROM pg_locks
			WHERE locktype = 'advisory' AND granted AND classid = hashtext('test.instance')::oid`).Scan(&n)
		if err != nil || n != 1 {
			t.Fatalf("terminating the lock's session: %d, %v", n, err)
		}
	}
	kill()
	select {
	case <-lost:
	case <-time.After(10 * time.Second):
		t.Fatal("the first instance did not notice that it lost the lock")
	}
	eventually(t, "one instance to hold the lock again", func() bool { return first.Held() || second.Held() })
	if first.Held() && second.Held() {
		t.Fatal("both instances hold the lock")
	}

	// Whoever holds it, stopping it hands the lock over.
	holder, other := first, second
	if second.Held() {
		holder, other = second, first
	}
	holder.Close()
	eventually(t, "the other instance to take over", other.Held)
}

// Close releases the lock for the next instance.
func TestInstanceLockClosePassesItOn(t *testing.T) {
	ctx := context.Background()
	pool, err := store.Open(ctx, storetest.NewDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	a := store.StartInstanceLock(ctx, pool.Config().ConnConfig, fast(store.InstanceLockOptions{BootWait: time.Second}))
	if !a.Held() {
		t.Fatal("no lock")
	}
	a.Close()
	if a.Held() {
		t.Error("a closed instance claims the lock")
	}
	b := store.StartInstanceLock(ctx, pool.Config().ConnConfig, fast(store.InstanceLockOptions{BootWait: time.Second}))
	defer b.Close()
	if !b.Held() {
		t.Error("the next instance did not get the lock Close released")
	}
}
