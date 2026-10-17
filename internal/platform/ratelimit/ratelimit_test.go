package ratelimit

import (
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newClock() *clock { return &clock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)} }

func TestLimiter(t *testing.T) {
	c := newClock()
	l := NewLimiter(60, 3) // one per second, burst 3
	l.now = c.now
	for i := 0; i < 3; i++ {
		if ok, _ := l.Allow("ip"); !ok {
			t.Fatalf("request %d refused within burst", i+1)
		}
	}
	ok, wait := l.Allow("ip")
	if ok || wait <= 0 || wait > time.Second {
		t.Fatalf("4th request: ok=%v wait=%v", ok, wait)
	}
	if ok, _ := l.Allow("other"); !ok {
		t.Fatal("keys are not independent")
	}
	c.advance(time.Second)
	if ok, _ := l.Allow("ip"); !ok {
		t.Fatal("bucket did not refill")
	}
}

// Review H-2: idle keys are swept by age, not only when the map is full.
func TestLimiterSweepsIdleKeys(t *testing.T) {
	c := newClock()
	l := NewLimiter(60, 2)
	l.now = c.now
	for i := 0; i < sweepEvery; i++ {
		l.Allow("k" + strconv.Itoa(i))
	}
	c.advance(time.Hour)
	for i := 0; i < sweepEvery; i++ {
		l.Allow("fresh")
	}
	if n := l.Len(); n > 2 {
		t.Fatalf("%d keys kept after they went idle", n)
	}
}

func step(b *Backoff, key string, success bool) time.Duration {
	a, wait := b.Begin(key)
	if a == nil {
		return wait
	}
	a.Done(success)
	return 0
}

func TestBackoffEscalates(t *testing.T) {
	c := newClock()
	b := NewBackoff(5, 15*time.Minute, time.Second, 15*time.Minute)
	b.now = c.now
	for i := 0; i < 5; i++ {
		if w := step(b, "a@example.com", false); w != 0 {
			t.Fatalf("attempt %d refused: %v", i+1, w)
		}
	}
	if _, w := b.Begin("a@example.com"); w != time.Second {
		t.Fatalf("after 5 failures: %v, want 1s", w)
	}
	c.advance(time.Second)
	step(b, "a@example.com", false) // 6th
	if _, w := b.Begin("a@example.com"); w != 2*time.Second {
		t.Fatalf("after 6 failures: %v, want 2s", w)
	}
	for i := 0; i < 40; i++ {
		c.advance(b.Max)
		step(b, "a@example.com", false)
	}
	if _, w := b.Begin("a@example.com"); w != 15*time.Minute {
		t.Fatalf("cap: %v", w)
	}
	c.advance(b.Max)
	step(b, "a@example.com", true)
	if a, w := b.Begin("a@example.com"); a == nil || w != 0 {
		t.Fatalf("success did not reset: %v", w)
	}
}

func TestBackoffWindowExpires(t *testing.T) {
	c := newClock()
	b := NewBackoff(3, time.Minute, time.Second, time.Minute)
	b.now = c.now
	step(b, "k", false)
	step(b, "k", false)
	c.advance(2 * time.Minute)
	step(b, "k", false)
	if _, w := b.Begin("k"); w != 0 {
		t.Fatalf("old failures still counted: %v", w)
	}
}

// Review M-2: attempts in flight count against the threshold, so a concurrent burst cannot
// run more checks than the threshold allows.
func TestBackoffCountsAttemptsInFlight(t *testing.T) {
	b := NewBackoff(5, 15*time.Minute, 30*time.Second, 15*time.Minute)
	var mu sync.Mutex
	var held []*Attempt
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if a, _ := b.Begin("victim@example.com"); a != nil {
				mu.Lock()
				held = append(held, a)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(held) != 5 {
		t.Fatalf("%d concurrent attempts admitted, want 5", len(held))
	}
	for _, a := range held {
		a.Done(false)
	}
	if a, _ := b.Begin("victim@example.com"); a != nil {
		t.Fatal("admitted after the threshold of failures")
	}
}

// Review H-2: huge attacker-supplied keys are stored as fixed-size hashes.
func TestLongKeysAreHashed(t *testing.T) {
	b := NewBackoff(5, time.Minute, time.Second, time.Minute)
	huge := strings.Repeat("x", 1<<20)
	step(b, huge, false)
	for k := range b.entries {
		if len(k) > maxKeyLen+1 {
			t.Fatalf("stored a %d-byte key", len(k))
		}
	}
	// The hashed key still identifies the same caller.
	for i := 0; i < 4; i++ {
		step(b, huge, false)
	}
	if a, _ := b.Begin(huge); a != nil {
		t.Fatal("hashed key lost its failures")
	}
}

func TestAddrKey(t *testing.T) {
	cases := map[string]string{
		"203.0.113.9":                  "203.0.113.9",
		"::ffff:203.0.113.9":           "203.0.113.9",
		"2001:db8:1:2:aaaa::1":         "2001:db8:1:2::/64",
		"2001:db8:1:2:ffff:ffff:0:abc": "2001:db8:1:2::/64",
	}
	for in, want := range cases {
		if got := AddrKey(netip.MustParseAddr(in)); got != want {
			t.Errorf("AddrKey(%s) = %s, want %s", in, got, want)
		}
	}
}
