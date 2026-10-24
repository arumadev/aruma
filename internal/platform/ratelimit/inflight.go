package ratelimit

import (
	"net/netip"
	"sync"
)

// InFlight counts requests in flight per client address, IPv6 per /64 (AddrKey). One InFlight
// is one budget: every API that shares it draws from it, so an address cannot double its limit
// by spreading its requests over two of them. It keeps an entry only while a request holds a
// place, so it needs no sweep. The zero value is ready to use.
type InFlight struct {
	mu sync.Mutex
	n  map[string]int
}

// Acquire takes a place for a request from ip, under limit; the caller calls release once the
// request is done, and a second call does nothing. The zero Addr, and a limit of 0 or less, are
// never limited.
func (c *InFlight) Acquire(ip netip.Addr, limit int) (release func(), ok bool) {
	if !ip.IsValid() || limit <= 0 {
		return func() {}, true
	}
	key := AddrKey(ip)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.n[key] >= limit {
		return nil, false
	}
	if c.n == nil {
		c.n = map[string]int{}
	}
	c.n[key]++
	return sync.OnceFunc(func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.n[key]--; c.n[key] <= 0 {
			delete(c.n, key)
		}
	}), true
}
