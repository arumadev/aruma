// Package ratelimit provides in-memory limits for a single server (U-15): a token bucket per
// key, and an escalating backoff per key after repeated failures.
package ratelimit

import (
	"math"
	"sync"
	"time"
)

// maxKeys bounds memory; idle entries are swept when it is exceeded.
const maxKeys = 100_000

// Limiter is a token bucket per key.
type Limiter struct {
	mu      sync.Mutex
	rate    float64 // tokens per second
	burst   float64
	buckets map[string]*bucket
	now     func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// NewLimiter allows burst requests at once, refilled at perMinute.
func NewLimiter(perMinute, burst int) *Limiter {
	return &Limiter{
		rate:    float64(perMinute) / 60,
		burst:   float64(burst),
		buckets: map[string]*bucket{},
		now:     time.Now,
	}
}

// Allow takes a token for key. When none is left it returns how long until one is.
func (l *Limiter) Allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= maxKeys {
			l.sweep(now)
		}
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens = math.Min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.rate)
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	wait := time.Duration((1 - b.tokens) / l.rate * float64(time.Second))
	return false, wait
}

func (l *Limiter) sweep(now time.Time) {
	full := time.Duration(l.burst / l.rate * float64(time.Second))
	for k, b := range l.buckets {
		if now.Sub(b.last) > full {
			delete(l.buckets, k)
		}
	}
}

// Backoff locks a key out for a growing delay once it fails Threshold times within Window:
// Base after the threshold, doubling with each further failure up to Max. A success resets it.
type Backoff struct {
	Threshold int
	Window    time.Duration
	Base      time.Duration
	Max       time.Duration

	mu      sync.Mutex
	entries map[string]*failures
	now     func() time.Time
}

type failures struct {
	count  int
	first  time.Time
	locked time.Time // locked until
}

// NewBackoff returns a backoff with the given policy.
func NewBackoff(threshold int, window, base, max time.Duration) *Backoff {
	return &Backoff{Threshold: threshold, Window: window, Base: base, Max: max, entries: map[string]*failures{}, now: time.Now}
}

// Wait returns how long key is still locked out, zero if it may try now.
func (b *Backoff) Wait(key string) time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	e, ok := b.entries[key]
	if !ok {
		return 0
	}
	if d := e.locked.Sub(b.now()); d > 0 {
		return d
	}
	return 0
}

// Fail records a failed attempt for key.
func (b *Backoff) Fail(key string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	e, ok := b.entries[key]
	if !ok || now.Sub(e.first) > b.Window && now.After(e.locked) {
		if !ok && len(b.entries) >= maxKeys {
			b.sweep(now)
		}
		e = &failures{first: now}
		b.entries[key] = e
	}
	e.count++
	if over := e.count - b.Threshold; over >= 0 {
		d := b.Base << min(over, 30)
		if d <= 0 || d > b.Max {
			d = b.Max
		}
		e.locked = now.Add(d)
	}
}

// Success clears key.
func (b *Backoff) Success(key string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.entries, key)
}

func (b *Backoff) sweep(now time.Time) {
	for k, e := range b.entries {
		if now.Sub(e.first) > b.Window && now.After(e.locked) {
			delete(b.entries, k)
		}
	}
}
