package core_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arumadev/aruma/internal/services/documents/core"
	"github.com/arumadev/aruma/internal/store/storetest"
)

// usageClock is a settable clock.
type usageClock struct{ t atomic.Pointer[time.Time] }

func (c *usageClock) now() time.Time      { return *c.t.Load() }
func (c *usageClock) set(t time.Time)     { c.t.Store(&t) }
func (c *usageClock) add(d time.Duration) { c.set(c.now().Add(d)) }

func hours(t *testing.T, u *core.Usage, project int64, since time.Time) []core.UsageHour {
	t.Helper()
	hs, err := u.Hours(t.Context(), project, since)
	if err != nil {
		t.Fatal(err)
	}
	return hs
}

// TestUsage: counts and peaks land in their project's hour, are read before and after a flush
// alike, survive a new process (a new Usage on the same table), keep the projects apart, are
// filtered by period, and the hours past the retention are pruned.
func TestUsage(t *testing.T) {
	pool := storetest.NewPool(t)
	var clock usageClock
	h0 := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	clock.set(h0.Add(5 * time.Minute))
	u := core.NewUsage(pool, clock.now, nil)
	const a, b = 101, 202

	// Concurrent counts, under -race.
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 100 {
				u.Read(a, 1)
			}
		})
	}
	wg.Wait()
	u.Wrote(a, []core.Write{{Kind: core.WriteUpdate}, {Kind: core.WriteDelete}, {Kind: core.WriteVerify}, {Kind: core.WriteUpdate}})
	u.Rule(a, core.RuleAllow)
	u.Rule(a, core.RuleDeny)
	u.Rule(a, core.RuleDeny)
	u.Rule(a, core.RuleError)
	u.Listeners(a, 3)
	u.Listeners(a, -2)
	u.Connections(a, 1)
	u.Read(b, 7)

	want := core.UsageHour{Hour: h0, Reads: 800, Writes: 2, Deletes: 1, ListenersPeak: 3, ConnectionsPeak: 1,
		RulesAllows: 1, RulesDenies: 2, RulesErrors: 1}
	if hs := hours(t, u, a, h0); len(hs) != 1 || hs[0] != want {
		t.Fatalf("before the flush: %+v", hs)
	}
	if err := u.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if hs := hours(t, u, a, h0); len(hs) != 1 || hs[0] != want {
		t.Fatalf("after the flush: %+v", hs)
	}
	if hs := hours(t, u, b, h0); len(hs) != 1 || hs[0] != (core.UsageHour{Hour: h0, Reads: 7}) {
		t.Errorf("the other project: %+v", hs)
	}
	if hs := hours(t, u, 303, h0); len(hs) != 0 {
		t.Errorf("a project that counted nothing: %+v", hs)
	}

	// The next hour: the listener and the connection still open are its peaks, though none
	// opened in it; a second flush adds to the first's counts.
	clock.add(time.Hour)
	u.Read(a, 5)
	if err := u.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	h1 := h0.Add(time.Hour)
	if hs := hours(t, u, a, h1); len(hs) != 1 || hs[0] != (core.UsageHour{Hour: h1, Reads: 5, ListenersPeak: 1, ConnectionsPeak: 1}) {
		t.Errorf("the next hour: %+v", hs)
	}
	u.Read(a, 1)
	if err := u.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}

	// A new process reads what the last one flushed.
	u2 := core.NewUsage(pool, clock.now, nil)
	hs := hours(t, u2, a, h0)
	if len(hs) != 2 || hs[0] != want || hs[1].Reads != 6 || hs[1].ListenersPeak != 1 {
		t.Errorf("after a restart: %+v", hs)
	}
	// The period starts at its first hour.
	if hs := hours(t, u2, a, h1.Add(30*time.Minute)); len(hs) != 1 || hs[0].Hour != h1 {
		t.Errorf("from the second hour: %+v", hs)
	}

	// Retention: 32 days on, the first hours are pruned at the next flush.
	clock.add(32 * 24 * time.Hour)
	u2.Read(a, 1)
	if err := u2.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM documents.usage_hours WHERE hour < $1`, clock.now().Add(-core.UsageRetention)).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d hours past the retention (%v)", n, err)
	}
	if hs := hours(t, u2, a, time.Time{}); len(hs) != 1 || hs[0].Reads != 1 {
		t.Errorf("after the pruning: %+v", hs)
	}
}

// TestUsageFlushFailure: counts a flush could not write are kept for the next.
func TestUsageFlushFailure(t *testing.T) {
	pool := storetest.NewPool(t)
	u := core.NewUsage(pool, nil, nil)
	u.Read(1, 3)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := u.Flush(ctx); err == nil {
		t.Fatal("a flush with a cancelled context succeeded")
	}
	if err := u.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if hs := hours(t, core.NewUsage(pool, nil, nil), 1, time.Now().Add(-time.Hour)); len(hs) != 1 || hs[0].Reads != 3 {
		t.Errorf("after the failed flush: %+v", hs)
	}
}

// TestUsageRun: Run's last flush, when its context ends (the process stops), keeps the counts
// for the next process.
func TestUsageRun(t *testing.T) {
	pool := storetest.NewPool(t)
	u := core.NewUsage(pool, nil, nil)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		u.Run(ctx, time.Hour)
		close(done)
	}()
	u.Wrote(5, []core.Write{{Kind: core.WriteUpdate}})
	cancel()
	<-done
	if hs := hours(t, core.NewUsage(pool, nil, nil), 5, time.Now().Add(-time.Hour)); len(hs) != 1 || hs[0].Writes != 1 {
		t.Errorf("after the process stopped: %+v", hs)
	}
}

// TestUsagePurge: after a project is purged, the next flush writes nothing for it: neither the
// counts it had not flushed, nor the listeners its open streams held. Another project is kept.
func TestUsagePurge(t *testing.T) {
	pool := storetest.NewPool(t)
	u := core.NewUsage(pool, nil, nil)
	const gone, kept = 301, 302
	u.Read(gone, 2)
	u.Listeners(gone, 1)
	u.Read(kept, 1)
	if err := u.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	u.Read(gone, 5)
	if err := core.PurgeProject(t.Context(), pool, u, gone); err != nil {
		t.Fatal(err)
	}
	u.Listeners(gone, -1) // the stream that was open closes
	if err := u.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	since := time.Now().Add(-time.Hour)
	if hs := hours(t, core.NewUsage(pool, nil, nil), gone, since); len(hs) != 0 {
		t.Errorf("the purged project's hours came back: %+v", hs)
	}
	if hs := hours(t, u, gone, since); len(hs) != 0 {
		t.Errorf("the purged project's hours, read with its counts in memory: %+v", hs)
	}
	if hs := hours(t, u, kept, since); len(hs) != 1 || hs[0].Reads != 1 {
		t.Errorf("the other project: %+v", hs)
	}
}
