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
	h       *Handler
	id      string // SID
	http    string // gsessionid, sent in X-HTTP-Session-Id
	client  netip.Addr
	project string // the project id the handshake's database names, as sent
	ctx     context.Context
	cancel  context.CancelCauseFunc
	expiry  *time.Timer

	mu sync.Mutex
	// changed is closed and replaced at every change a waiter may be waiting for.
	changed chan struct{}

	// The forward channel: the id of the last map taken, and the requests not yet read.
	lastMap      int
	in           [][]byte
	inBytes      int64
	readDeadline time.Time

	// The backchannel: the next array's id (the handshake's is 0), the arrays not acknowledged
	// in id order, and the backchannel attached, if any.
	nextID        int
	out           []array
	outBytes      int64
	writeDeadline time.Time
	back          *backchannel
	backed        bool // a backchannel was attached once
	// ended is set when the stream has ended: its status, if any, is the last array.
	ended bool
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
// a retried request is not taken twice. It waits while the requests not yet read hold
// message_bytes, for at most stall: a stream that does not read is its own to bound.
func (s *session) take(maps []fmap, stall time.Duration) error {
	deadline := time.Now().Add(stall)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range maps {
		if m.id <= s.lastMap {
			continue
		}
		n := int64(len(m.data))
		for s.inBytes > 0 && s.inBytes+n > s.h.limits.MessageBytes {
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
		}
		s.in = append(s.in, m.data)
		s.inBytes += n
		s.lastMap = m.id
		s.wakeLocked()
	}
	return nil
}

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
			s.inBytes -= int64(len(b))
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

// end records the stream's end, and its status as the last arrays (closing).
func (s *session) end(status *wire.Error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ended = true
	if status != nil && s.ctx.Err() == nil {
		for _, p := range closing(status.RESTBody()) {
			s.appendLocked(p)
		}
	}
	s.wakeLocked()
}

// checkExpiry ends a session idle for the handler's expiry: no backchannel attached and no
// request since. The SDK keeps a backchannel open, or a long poll pending, for as long as the
// session lives, so a session without either is one whose client went away without terminating
// it.
func (s *session) checkExpiry() {
	s.mu.Lock()
	idle := s.h.timing.Expiry - time.Since(s.active)
	if s.back != nil {
		idle = s.h.timing.Expiry
	}
	s.mu.Unlock()
	if idle > 0 {
		s.expiry.Reset(idle)
		return
	}
	s.h.remove(s, errExpired)
}
