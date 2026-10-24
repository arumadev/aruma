package ratelimit

import (
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
)

// The requests in flight from one address, IPv6 per /64, under a limit; a release frees its
// place once, however often it is called; the zero value is ready.
func TestInFlight(t *testing.T) {
	var c InFlight
	a := netip.MustParseAddr("2001:db8::1")
	b := netip.MustParseAddr("2001:db8::2") // the same /64
	r1, ok1 := c.Acquire(a, 2)
	r2, ok2 := c.Acquire(b, 2)
	_, ok3 := c.Acquire(a, 2)
	if !ok1 || !ok2 || ok3 {
		t.Fatalf("acquire: %v %v %v", ok1, ok2, ok3)
	}
	if _, ok := c.Acquire(netip.MustParseAddr("2001:db8:0:1::1"), 2); !ok {
		t.Error("another /64 is limited")
	}
	if _, ok := c.Acquire(netip.MustParseAddr("192.0.2.1"), 2); !ok {
		t.Error("another address is limited")
	}
	r1()
	r1()
	if _, ok := c.Acquire(a, 2); !ok {
		t.Error("a released place is not free")
	}
	if _, ok := c.Acquire(a, 2); ok {
		t.Error("a double release freed two places")
	}
	r2()
	if _, ok := c.Acquire(netip.Addr{}, 1); !ok {
		t.Error("the zero address is limited")
	}
	if _, ok := c.Acquire(a, 0); !ok {
		t.Error("a limit of 0 limits")
	}
}

// Under concurrency the count never passes the limit, and every place comes back.
func TestInFlightConcurrent(t *testing.T) {
	var c InFlight
	ip := netip.MustParseAddr("192.0.2.9")
	const limit = 4
	var held, peak atomic.Int64
	var wg sync.WaitGroup
	for range 64 {
		wg.Go(func() {
			for range 100 {
				release, ok := c.Acquire(ip, limit)
				if !ok {
					continue
				}
				n := held.Add(1)
				for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
				}
				held.Add(-1)
				release()
			}
		})
	}
	wg.Wait()
	if peak.Load() > limit {
		t.Errorf("%d in flight under a limit of %d", peak.Load(), limit)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.n) != 0 {
		t.Errorf("entries left after every release: %v", c.n)
	}
}
