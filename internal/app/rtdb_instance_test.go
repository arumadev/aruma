package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/arumadev/aruma/internal/config"
	rtdb "github.com/arumadev/aruma/internal/services/rtdb/core"
	"github.com/arumadev/aruma/internal/store"
	"github.com/arumadev/aruma/internal/store/storetest"
)

// Two processes on one database: only the one that holds the Realtime Database's lock serves
// it (the other answers UNAVAILABLE, and /readyz 503), because each keeps its listeners in
// memory and one would store writes the other's listeners never hear of. When the holder loses
// its Postgres session it drops its connections and refuses new ones until the lock is taken
// again by one of the two.
func TestRTDBSingleInstance(t *testing.T) {
	ctx := context.Background()
	url := storetest.NewDB(t)
	pool, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	cfg, err := config.Load("", func(k string) string {
		return map[string]string{
			"ARUMA_DATABASE_URL": url, "ARUMA_PUBLIC_URL": "https://aruma.test", "ARUMA_RTDB_ENABLED": "true",
			"ARUMA_MASTER_KEY": "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
		}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	newApp := func() *App {
		a, err := New(cfg, log, "test", pool)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	ready := func(a *App) int {
		rec := httptest.NewRecorder()
		a.Server.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
		return rec.Code
	}
	opts := store.InstanceLockOptions{Check: 20 * time.Millisecond, MinBackoff: 20 * time.Millisecond, MaxBackoff: 50 * time.Millisecond}

	a, b := newApp(), newApp()
	if !a.RTDB.Serving() || ready(a) != http.StatusOK {
		t.Fatal("an app serves the Realtime Database from the start, before BootRTDB (tests rely on it)")
	}
	opts.BootWait = time.Second
	a.BootRTDB(ctx, pool, opts)
	defer a.rtdbLock.Close()
	opts.BootWait = 100 * time.Millisecond
	b.BootRTDB(ctx, pool, opts)
	defer b.rtdbLock.Close()

	if !a.RTDB.Serving() || ready(a) != http.StatusOK {
		t.Fatalf("the first instance does not serve: serving %v, readyz %d", a.RTDB.Serving(), ready(a))
	}
	if b.RTDB.Serving() || ready(b) != http.StatusServiceUnavailable {
		t.Fatalf("the second instance serves while the first holds the lock: serving %v, readyz %d", b.RTDB.Serving(), ready(b))
	}
	if _, err := b.RTDB.Resolve(ctx, "chat-default-rtdb"); !errors.Is(err, rtdb.ErrUnavailable) {
		t.Errorf("the second instance resolves a database: %v", err)
	}

	// The holder's Postgres session ends. It stops serving; one of the two serves afterwards.
	if _, err := pool.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_locks
		WHERE locktype = 'advisory' AND granted AND classid = hashtext('rtdb.instance')::oid`); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the holder to notice the loss", func() bool { return !a.RTDB.Serving() || a.rtdbLock.Held() && b.rtdbLock.Held() })
	waitFor(t, "an instance to serve again", func() bool { return a.RTDB.Serving() != b.RTDB.Serving() })
	if a.RTDB.Serving() != a.rtdbLock.Held() || b.RTDB.Serving() != b.rtdbLock.Held() {
		t.Error("serving and holding the lock disagree")
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}
