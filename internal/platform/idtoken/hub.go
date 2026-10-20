package idtoken

import (
	"errors"
	"sync"
)

// DefaultBuffer is how many revocations a subscription holds before it overflows.
const DefaultBuffer = 256

// ErrOverflow ends a subscription whose reader fell more than its buffer behind. Revocations
// were lost, so the reader re-verifies every session it holds, then subscribes again.
var ErrOverflow = errors.New("idtoken: revocations lost: subscriber fell behind")

// Hub fans revocations out to subscribers in-process. It is Auth's Revocations; a test can use
// one on its own.
//
// Publish never blocks the change that triggered it: each subscription has a bounded buffer,
// and a subscription that is full is ended with ErrOverflow rather than waited for. One
// Publish costs one non-blocking send per subscriber of that project.
type Hub struct {
	buffer int

	mu   sync.Mutex
	subs map[int64]map[*Subscription]struct{}
}

// NewHub returns a hub whose subscriptions buffer up to buffer revocations (DefaultBuffer if
// buffer < 1).
func NewHub(buffer int) *Hub {
	if buffer < 1 {
		buffer = DefaultBuffer
	}
	return &Hub{buffer: buffer, subs: map[int64]map[*Subscription]struct{}{}}
}

// Subscription is one consumer's stream of a project's revocations.
type Subscription struct {
	hub     *Hub
	project int64
	ch      chan Revocation
	err     error // set under hub.mu when ch is closed
}

// Subscribe implements Revocations.
func (h *Hub) Subscribe(project int64) *Subscription {
	s := &Subscription{hub: h, project: project, ch: make(chan Revocation, h.buffer)}
	h.mu.Lock()
	defer h.mu.Unlock()
	set := h.subs[project]
	if set == nil {
		set = map[*Subscription]struct{}{}
		h.subs[project] = set
	}
	set[s] = struct{}{}
	return s
}

// Publish delivers r to every subscriber of its project without waiting for any of them.
func (h *Hub) Publish(r Revocation) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs[r.Project] {
		select {
		case s.ch <- r:
		default:
			h.end(s, ErrOverflow)
		}
	}
}

// Subscribers returns how many subscriptions the project has.
func (h *Hub) Subscribers(project int64) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs[project])
}

// end removes s and closes its channel; h.mu is held.
func (h *Hub) end(s *Subscription, err error) {
	set := h.subs[s.project]
	if _, ok := set[s]; !ok {
		return
	}
	delete(set, s)
	if len(set) == 0 {
		delete(h.subs, s.project)
	}
	s.err = err
	close(s.ch)
}

// C delivers the revocations. It is closed when the subscription ends: by Close, or by an
// overflow, which Err then reports.
func (s *Subscription) C() <-chan Revocation { return s.ch }

// Err is ErrOverflow once an overflow ended the subscription, else nil.
func (s *Subscription) Err() error {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	return s.err
}

// Close ends the subscription. It is safe to call more than once and after an overflow.
func (s *Subscription) Close() {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	s.hub.end(s, nil)
}
