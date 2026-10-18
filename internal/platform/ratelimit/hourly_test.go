package ratelimit

import (
	"testing"
	"time"
)

func TestHourlyLimiter(t *testing.T) {
	c := newClock()
	l := NewHourlyLimiter(6, 2) // one per ten minutes, burst 2
	l.now = c.now
	for i := 0; i < 2; i++ {
		if ok, _ := l.Allow("a@example.com"); !ok {
			t.Fatalf("request %d refused within burst", i+1)
		}
	}
	ok, wait := l.Allow("a@example.com")
	if ok || wait <= 9*time.Minute || wait > 10*time.Minute {
		t.Fatalf("3rd request: ok=%v wait=%v", ok, wait)
	}
	c.advance(10 * time.Minute)
	if ok, _ := l.Allow("a@example.com"); !ok {
		t.Fatal("bucket did not refill")
	}
}
