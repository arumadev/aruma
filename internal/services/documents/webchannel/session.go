package webchannel

import (
	"context"
	"errors"
	"io"
	"net/netip"
	"os"
	"sync"
	"time"

	"github.com/arumadev/aruma/internal/services/documents/wire"
)

// session is one WebChannel session: one stream, Listen or Write, whose requests arrive on the
// forward channel's POSTs and whose responses leave on the backchannel's GETs, any number of each
// over its life. It holds the requests the stream has not read, and the arrays the client has not
// acknowledged (AID), so that a backchannel that broke is sent again from the client's last
// array.
type session struct {
	h      *Handler
	id     string // SID
	http   string // gsessionid, sent in X-HTTP-Session-Id
	client netip.Addr
	ctx    context.Context
	cancel context.CancelCauseFunc
	expiry *time.Timer

	mu sync.Mutex
	// changed is closed and replaced at every change a waiter may be waiting for.
	changed chan struct{}

	// The forward channel: the id of the last map taken, the requests not yet read, and the maps
	// that came before one of a lower id, held until it comes (pendBytes counts them, mapCost
	// each). forwarding is held by the forward POST reading its body and taking its maps.
	lastMap      int
	in           [][]byte
	inBytes      int64
	pending      map[int][]byte
	pendBytes    int64
	readDeadline time.Time
	forwarding   chan struct{}

	// The backchannel: the next array's id (the handshake's is 0), the arrays not acknowledged
	// in id order, and the backchannel attached, if any.
	nextID        int
	out           []array
	outBytes      int64
	writeDeadline time.Time
	back          *backchannel
	backed        bool // a backchannel was attached once
	// ended is when the stream ended, or zero: its status, if any, is the last array.
	ended time.Time
	// active is when a request of the session was last seen, or its backchannel detached.
	active  time.Time
	removed bool
}

// backchannel is one backchannel GET attached to a session.
type backchannel struct {
	next     int  // the first array id it has not written
	replaced bool // a newer backchannel took its place
	first    bool // the session's first backchannel
}

// Errors that end a session from outside its stream; never sent.
var (
	errTerminated = errors.New("webchannel: the client terminated the session")
	errExpired    = errors.New("webchannel: the session expired")
)

func (s *session) wakeLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
}

// await waits for a change after ch was taken, until deadline (zero is none), or the session's end.
func (s *session) await(ch <-chan struct{}, deadline time.Time) {
	var timeout <-chan time.Time
	if !deadline.IsZero() {
		t := time.NewTimer(time.Until(deadline))
		defer t.Stop()
		timeout = t.C
	}
	select {
	case <-ch:
	case <-timeout:
	case <-s.ctx.Done():
	}
}

// touchLocked marks the session active now.
func (s *session) touchLocked() { s.active = time.Now() }

// ackLocked drops the arrays the client has received: every one up to aid.
func (s *session) ackLocked(aid int) {
	if aid >= s.nextID {
		return // an id never sent
	}
	n := 0
	for n < len(s.out) && s.out[n].id <= aid {
		s.outBytes -= int64(len(s.out[n].payload))
		s.out[n] = array{}
		n++
	}
	if n > 0 {
		s.out = s.out[n:]
		s.wakeLocked()
	}
}

// take queues the maps of a forward channel request the session has not taken yet, in id order:
// a retried request is not taken twice, and a map that comes before one of a lower id is held
// until that one comes, as several forward POSTs may be in flight at once and a retried map is
// sent again with a new one (ForwardChannelRequestPool, WireV8.encodeMessageQueue). It waits
// while the requests not yet read, held maps included, would pass message_bytes, for at most
// stall: a stream that does not read is its own to bound. A held map that does not fit while the
// stream has nothing to read waits for nothing that can come but the map before it, which the
// caller's own POST holds back: it is refused at once. After the stream ended, maps are dropped.
func (s *session) take(maps []fmap, stall time.Duration) error {
	deadline := time.Now().Add(stall)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range maps {
		if !s.ended.IsZero() {
			return nil
		}
		if m.id <= s.lastMap {
			continue
		}
		if _, ok := s.pending[m.id]; ok {
			continue
		}
		n := mapCost(m.data)
		next := m.id == s.lastMap+1
		for {
			held := s.inBytes + s.pendBytes
			if held == 0 || held+n <= s.h.limits.MessageBytes || next && s.inBytes == 0 {
				break
			}
			if !next && s.inBytes == 0 {
				return errBusy
			}
			if s.ctx.Err() != nil {
				return context.Cause(s.ctx)
			}
			if !time.Now().Before(deadline) {
				return errBusy
			}
			ch := s.changed
			s.mu.Unlock()
			s.await(ch, deadline)
			s.mu.Lock()
			if !s.ended.IsZero() {
				return nil
			}
		}
		if !next {
			if s.pending == nil {
				s.pending = map[int][]byte{}
			}
			s.pending[m.id] = m.data
			s.pendBytes += n
			continue
		}
		s.in = append(s.in, m.data)
		s.inBytes += n
		s.lastMap = m.id
		// The maps held after it, as far as they run on.
		for {
			data, ok := s.pending[s.lastMap+1]
			if !ok {
				break
			}
			delete(s.pending, s.lastMap+1)
			c := mapCost(data)
			s.pendBytes -= c
			s.in = append(s.in, data)
			s.inBytes += c
			s.lastMap++
		}
		s.wakeLocked()
	}
	return nil
}

// mapCost is what a map counts against message_bytes: its bytes, and a little for holding it, so
// that empty maps are not free.
func mapCost(data []byte) int64 { return int64(len(data)) + 64 }

var errBusy = errors.New("webchannel: the stream is not reading its requests")

// receive returns the stream's next request: io.EOF never (a session's client does not end its
// side but by terminating it, which ends the stream), os.ErrDeadlineExceeded past the read
// deadline.
func (s *session) receive() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		if len(s.in) > 0 {
			b := s.in[0]
			s.in[0] = nil
			s.in = s.in[1:]
			s.inBytes -= mapCost(b)
			s.wakeLocked()
			return b, nil
		}
		if s.ctx.Err() != nil {
			return nil, io.EOF
		}
		if !s.readDeadline.IsZero() && !time.Now().Before(s.readDeadline) {
			return nil, os.ErrDeadlineExceeded
		}
		ch, deadline := s.changed, s.readDeadline
		s.mu.Unlock()
		s.await(ch, deadline)
		s.mu.Lock()
	}
}

// send queues a payload as the next array. It waits while the arrays not acknowledged hold
// session_bytes, until the write deadline: a client that does not read (or acknowledge) is
// bounded as on gRPC, where the stream's outbox gives up after stream_write_stall.
func (s *session) send(payload []byte) error {
	n := int64(len(payload))
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		if s.ctx.Err() != nil {
			return context.Cause(s.ctx)
		}
		if !s.writeDeadline.IsZero() && !time.Now().Before(s.writeDeadline) {
			return os.ErrDeadlineExceeded
		}
		if s.outBytes == 0 || s.outBytes+n <= s.h.limits.SessionBytes {
			s.appendLocked(payload)
			return nil
		}
		ch, deadline := s.changed, s.writeDeadline
		s.mu.Unlock()
		s.await(ch, deadline)
		s.mu.Lock()
	}
}

func (s *session) appendLocked(payload []byte) {
	s.out = append(s.out, array{id: s.nextID, payload: payload})
	s.nextID++
	s.outBytes += int64(len(payload))
	s.wakeLocked()
}

// SetReadDeadline and SetWriteDeadline are the stream's deadlines (grpc.Deadlines).
func (s *session) SetReadDeadline(t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readDeadline = t
	s.wakeLocked()
	return nil
}

func (s *session) SetWriteDeadline(t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writeDeadline = t
	s.wakeLocked()
	return nil
}

// end records the stream's end, and its status as the last arrays (closing). The requests it did
// not read are dropped, and the session is kept only for its client to read those arrays: for
// endLinger, which no request extends.
func (s *session) end(status *wire.Error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ended = time.Now()
	clear(s.in)
	s.in, s.inBytes = nil, 0
	s.pending, s.pendBytes = nil, 0
	if status != nil && s.ctx.Err() == nil {
		for _, p := range closing(status.RESTBody()) {
			s.appendLocked(p)
		}
	}
	s.expiry.Reset(s.h.endLinger())
	s.wakeLocked()
}

// endLinger is how long a session is kept after its stream ended: the expiry, at most 10 seconds.
func (h *Handler) endLinger() time.Duration { return min(h.timing.Expiry, 10*time.Second) }

// checkExpiry ends a session idle for the handler's expiry: no backchannel attached and no
// request since. The SDK keeps a backchannel open, or a long poll pending, for as long as the
// session lives, so a session without either is one whose client went away without terminating
// it. A session whose stream ended goes endLinger after the end, backchannel or not.
func (s *session) checkExpiry() {
	s.mu.Lock()
	var idle time.Duration
	switch {
	case !s.ended.IsZero():
		idle = s.h.endLinger() - time.Since(s.ended)
	case s.back != nil:
		idle = s.h.timing.Expiry
	default:
		idle = s.h.timing.Expiry - time.Since(s.active)
	}
	s.mu.Unlock()
	if idle > 0 {
		s.expiry.Reset(idle)
		return
	}
	s.h.remove(s, errExpired)
}

// acquireForward waits for the session's forward POST in progress, if any, to be done: for at
// most stall, the request's life, and the session's.
func (s *session) acquireForward(ctx context.Context, stall time.Duration) error {
	select {
	case s.forwarding <- struct{}{}:
		return nil
	default:
	}
	t := time.NewTimer(stall)
	defer t.Stop()
	select {
	case s.forwarding <- struct{}{}:
		return nil
	case <-t.C:
		return errBusy
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-s.ctx.Done():
		return context.Cause(s.ctx)
	}
}
