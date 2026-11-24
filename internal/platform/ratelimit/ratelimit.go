// Package ratelimit provides in-memory limits for a single server (U-15): a token bucket per
// key, and an escalating backoff per key after repeated failures.
//
// Keys are often attacker-chosen (an email typed into a login form), so both structures bound
// what they keep: long keys are stored as their SHA-256, idle entries are swept by age on a
// regular cadence, and the number of entries is capped.
package ratelimit

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"net/netip"
	"sync"
	"time"
)

const (
	// maxKeys caps the entries of one limiter.
	maxKeys = 100_000
	// sweepEvery is how many operations pass between age-based sweeps.
	sweepEvery = 1024
	// maxKeyLen is the longest key stored verbatim; longer ones are hashed.
	maxKeyLen = 64
)

// boundKey returns key, or its SHA-256 when it is long.
func boundKey(key string) string {
	if len(key) <= maxKeyLen {
		return key
	}
	sum := sha256.Sum256([]byte(key))
	return "#" + hex.EncodeToString(sum[:])
}

// AddrKey returns the rate-limit key of a client address. IPv6 clients are grouped by /64,
// the smallest block an ISP hands out, so one host cannot multiply its budget by rotating
// addresses within it.
func AddrKey(ip netip.Addr) string {
	if ip.Is6() && !ip.Is4In6() {
		p, _ := ip.Prefix(64)
		return p.String()
	}
	return ip.Unmap().String()
}

// Limiter is a token bucket per key.
type Limiter struct {
	mu      sync.Mutex
	rate    float64 // tokens per second
	burst   float64
	buckets map[string]*bucket
	// order holds the buckets most recently used first, so the least recently used one, and
	// every bucket that has refilled completely (they are the oldest), is found at the back
	// without scanning the table: a full limiter must stay cheap for a flood of new keys.
	order *list.List
	ops   int
	now   func() time.Time
}

type bucket struct {
	key    string
	tokens float64
	last   time.Time
	elem   *list.Element
}

// NewLimiter allows burst requests at once, refilled at perMinute.
func NewLimiter(perMinute, burst int) *Limiter {
	return &Limiter{
		rate:    float64(perMinute) / 60,
		burst:   float64(burst),
		buckets: map[string]*bucket{},
		order:   list.New(),
		now:     time.Now,
	}
}

// Allow takes a token for key. When none is left it returns how long until one is.
func (l *Limiter) Allow(key string) (bool, time.Duration) {
	key = boundKey(key)
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if l.ops++; l.ops%sweepEvery == 0 {
		l.sweep(now)
	}
	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= maxKeys {
			l.sweep(now)
			if len(l.buckets) >= maxKeys {
				// Full of active keys: the least recently used bucket makes room. Refusing
				// newcomers instead would let a flood of addresses lock everyone else out.
				if back := l.order.Back(); back != nil {
					l.remove(back.Value.(*bucket))
				}
			}
		}
		b = &bucket{key: key, tokens: l.burst, last: now}
		b.elem = l.order.PushFront(b)
		l.buckets[key] = b
	}
	b.tokens = math.Min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.rate)
	b.last = now
	l.order.MoveToFront(b.elem)
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	wait := time.Duration((1 - b.tokens) / l.rate * float64(time.Second))
	return false, wait
}

// sweep drops buckets that have refilled completely; they hold no state worth keeping. The
// list is ordered by last use, so it stops at the first bucket still filling: a sweep costs
// what it removes, not the size of the table.
func (l *Limiter) sweep(now time.Time) {
	full := time.Duration(l.burst / l.rate * float64(time.Second))
	for e := l.order.Back(); e != nil; e = l.order.Back() {
		b := e.Value.(*bucket)
		if now.Sub(b.last) <= full {
			return
		}
		l.remove(b)
	}
}

func (l *Limiter) remove(b *bucket) {
	l.order.Remove(b.elem)
	delete(l.buckets, b.key)
}

// Len reports the number of tracked keys.
func (l *Limiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}

// Backoff locks a key out for a growing delay once it fails Threshold times with less than
// Window between failures: Base after the threshold, doubling with each further failure up to
// Max. The window slides from the last failure, so steady guessing keeps escalating. A success
// resets it.
//
// Attempts are reserved with Begin before the slow check runs, so concurrent attempts count
// against the threshold too and a burst cannot slip past it.
type Backoff struct {
	Threshold int
	Window    time.Duration
	Base      time.Duration
	Max       time.Duration

	mu      sync.Mutex
	entries map[string]*failures
	// order holds the entries by last failure, newest first (see Limiter.order).
	order *list.List
	ops   int
	now   func() time.Time
}

type failures struct {
	key      string
	elem     *list.Element
	count    int
	inflight int
	last     time.Time // last failure; the window slides from it
	locked   time.Time // locked until
}

// NewBackoff returns a backoff with the given policy.
func NewBackoff(threshold int, window, base, max time.Duration) *Backoff {
	return &Backoff{Threshold: threshold, Window: window, Base: base, Max: max, entries: map[string]*failures{}, order: list.New(), now: time.Now}
}

// Attempt is a reserved attempt; call Done exactly once with its outcome.
type Attempt struct {
	b   *Backoff
	key string
}

// Begin reserves an attempt for key. When the key is locked out, or the attempts already in
// flight use up what the threshold allows, it returns how long to wait and no attempt.
func (b *Backoff) Begin(key string) (*Attempt, time.Duration) {
	key = boundKey(key)
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	if b.ops++; b.ops%sweepEvery == 0 {
		b.sweep(now)
	}
	e := b.entry(key, now)
	if e == nil {
		return nil, time.Second
	}
	if d := e.locked.Sub(now); d > 0 {
		return nil, d
	}
	// Below the threshold, attempts in flight may only use what is left of it; past it, one
	// attempt at a time once each lockout ends.
	if e.inflight >= max(1, b.Threshold-e.count) {
		return nil, b.Base
	}
	e.inflight++
	return &Attempt{b: b, key: key}, 0
}

// Done records the outcome of the attempt.
func (a *Attempt) Done(success bool) {
	b := a.b
	b.mu.Lock()
	defer b.mu.Unlock()
	e, ok := b.entries[a.key]
	if !ok {
		return
	}
	e.inflight--
	if success {
		b.remove(e)
		return
	}
	b.fail(e, b.now())
}

// entry returns the live entry for key, starting a new window when the old one has lapsed.
// When the map is full of active entries, the least recently failed one without an attempt in
// flight makes room: refusing every new key instead would let a few thousand addresses block
// password sign-in for everyone. It returns nil only when every entry has an attempt in flight.
func (b *Backoff) entry(key string, now time.Time) *failures {
	e, ok := b.entries[key]
	if ok && (e.inflight > 0 || now.Sub(e.last) <= b.Window || now.Before(e.locked)) {
		return e
	}
	if !ok && len(b.entries) >= maxKeys {
		b.sweep(now)
		if len(b.entries) >= maxKeys && !b.evictOldest() {
			return nil
		}
	}
	if ok {
		// A lapsed window starts afresh in the same entry.
		b.remove(e)
	}
	e = &failures{key: key, last: now}
	e.elem = b.order.PushFront(e)
	b.entries[key] = e
	return e
}

func (b *Backoff) remove(e *failures) {
	b.order.Remove(e.elem)
	delete(b.entries, e.key)
}

func (b *Backoff) fail(e *failures, now time.Time) {
	e.count++
	e.last = now
	b.order.MoveToFront(e.elem)
	if over := e.count - b.Threshold; over >= 0 {
		d := b.Base << min(over, 30)
		if d <= 0 || d > b.Max {
			d = b.Max
		}
		e.locked = now.Add(d)
	}
}

// evictOldest drops the entry whose last failure is oldest among those with no attempt in
// flight. It reports whether it found one.
func (b *Backoff) evictOldest() bool {
	for el := b.order.Back(); el != nil; el = el.Prev() {
		if e := el.Value.(*failures); e.inflight == 0 {
			b.remove(e)
			return true
		}
	}
	return false
}

// sweep drops the entries whose window has lapsed. They are ordered by last failure, so it
// stops at the first entry still inside its window.
func (b *Backoff) sweep(now time.Time) {
	for el := b.order.Back(); el != nil; {
		e := el.Value.(*failures)
		if now.Sub(e.last) <= b.Window {
			return
		}
		prev := el.Prev()
		if e.inflight == 0 && now.After(e.locked) {
			b.remove(e)
		}
		el = prev
	}
}

// Len reports the number of tracked keys.
func (b *Backoff) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.entries)
}
