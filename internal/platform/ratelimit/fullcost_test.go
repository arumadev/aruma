package ratelimit

import (
	"fmt"
	"testing"
	"time"
)

// A table full of active keys must not make every newcomer scan it: keys are attacker-chosen
// (an email typed into a login form), and both structures run under one mutex, so a scan per
// request lets a flood of new keys stall every other caller. Before the keys were kept in
// recency order, a newcomer to a full table cost two scans of it (about 17 ms at 100,000 keys).
func TestFullTableNewcomersAreCheap(t *testing.T) {
	const newcomers = 2000
	budget := 2 * time.Second // 1 ms each; a scan of 100,000 entries takes several

	l := NewLimiter(60, 10)
	for i := range maxKeys {
		l.Allow(fmt.Sprintf("k%d", i))
	}
	start := time.Now()
	for i := range newcomers {
		l.Allow(fmt.Sprintf("new%d", i))
	}
	if d := time.Since(start); d > budget {
		t.Errorf("limiter: %d new keys into a full table took %v, want under %v", newcomers, d, budget)
	}
	if l.Len() > maxKeys {
		t.Errorf("limiter holds %d keys, over %d", l.Len(), maxKeys)
	}

	b := NewBackoff(5, 15*time.Minute, 30*time.Second, 15*time.Minute)
	for i := range maxKeys {
		a, _ := b.Begin(fmt.Sprintf("e%d@example.com", i))
		a.Done(false)
	}
	start = time.Now()
	for i := range newcomers {
		a, _ := b.Begin(fmt.Sprintf("n%d@example.com", i))
		if a == nil {
			t.Fatal("a newcomer was refused by a table of idle entries")
		}
		a.Done(false)
	}
	if d := time.Since(start); d > budget {
		t.Errorf("backoff: %d new keys into a full table took %v, want under %v", newcomers, d, budget)
	}
	if b.Len() > maxKeys {
		t.Errorf("backoff holds %d keys, over %d", b.Len(), maxKeys)
	}
}
