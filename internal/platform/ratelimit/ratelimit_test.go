package ratelimit

import (
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }
func newClock() *clock                   { return &clock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)} }

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

func TestBackoffEscalates(t *testing.T) {
	c := newClock()
	b := NewBackoff(5, 15*time.Minute, time.Second, 15*time.Minute)
	b.now = c.now
	for i := 0; i < 4; i++ {
		b.Fail("a@example.com")
	}
	if w := b.Wait("a@example.com"); w != 0 {
		t.Fatalf("locked before threshold: %v", w)
	}
	b.Fail("a@example.com") // 5th failure
	if w := b.Wait("a@example.com"); w != time.Second {
		t.Fatalf("after 5 failures: %v, want 1s", w)
	}
	c.advance(time.Second)
	b.Fail("a@example.com") // 6th
	if w := b.Wait("a@example.com"); w != 2*time.Second {
		t.Fatalf("after 6 failures: %v, want 2s", w)
	}
	for i := 0; i < 40; i++ {
		b.Fail("a@example.com")
	}
	if w := b.Wait("a@example.com"); w != 15*time.Minute {
		t.Fatalf("cap: %v", w)
	}
	b.Success("a@example.com")
	if w := b.Wait("a@example.com"); w != 0 {
		t.Fatalf("success did not reset: %v", w)
	}
}

func TestBackoffWindowExpires(t *testing.T) {
	c := newClock()
	b := NewBackoff(3, time.Minute, time.Second, time.Minute)
	b.now = c.now
	b.Fail("k")
	b.Fail("k")
	c.advance(2 * time.Minute)
	b.Fail("k")
	if w := b.Wait("k"); w != 0 {
		t.Fatalf("old failures still counted: %v", w)
	}
}
