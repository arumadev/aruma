package api

import (
	"sync"

	"github.com/osama300/aurma-project/internal/services/rtdb/core"
)

// session is the protocol side of one client, apart from any socket: it turns requests into
// calls on its core.Session and puts replies and pushes, in order, into its outbox, which
// the transport drains. A transport hands it whole messages; it never sees frames.
type session struct {
	api    *API
	target core.Target
	core   *core.Session
	out    *outbox
	// id is the session id of the handshake; clients send it back as ls= on reconnect.
	id string

	mu      sync.Mutex
	pending []*slot // replies not sent yet, in request order
}

func newSession(a *API, target core.Target, cs *core.Session, outboxBytes int64) *session {
	return &session{api: a, target: target, core: cs, out: newOutbox(outboxBytes), id: sessionID()}
}

// slot holds the reply to one request until every earlier reply is out. Requests the writer
// runs complete later than those answered on the spot, but the client receives the replies in
// the order it asked, as from Firebase (pipelined requests are answered in order,
// B-emulator-oracle.md §1.3). Pushes do not wait: a write's push reaches the outbox before
// its ack, since the writer publishes before it replies.
type slot struct {
	msg  []byte
	done bool
}

// reserve takes the next reply slot.
func (s *session) reserve() *slot {
	s.mu.Lock()
	defer s.mu.Unlock()
	sl := &slot{}
	s.pending = append(s.pending, sl)
	return sl
}

// fill completes a slot and sends every reply that is now first in line.
func (s *session) fill(sl *slot, msg []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sl.msg, sl.done = msg, true
	n := 0
	for n < len(s.pending) && s.pending[n].done {
		s.out.push(s.pending[n].msg)
		n++
	}
	s.pending = s.pending[n:]
}

// send queues a message that answers no request, such as a pong or an error frame.
func (s *session) send(msg []byte) { s.out.push(msg) }

// close ends the session: its onDisconnect writes run, and its database is released.
func (s *session) close() {
	s.core.Close()
	s.out.close()
}

// outbox is the queue of messages waiting for the transport, bounded in bytes (ADR-019): a
// client that reads too slowly is disconnected rather than allowed to grow the server's
// memory, and resynchronises when it reconnects.
type outbox struct {
	mu         sync.Mutex
	max        int64
	size       int64
	msgs       [][]byte
	closed     bool
	overflowed bool
	ready      chan struct{}
}

func newOutbox(max int64) *outbox {
	return &outbox{max: max, ready: make(chan struct{}, 1)}
}

// push queues msg; false when the outbox is closed or msg does not fit, which closes it.
func (o *outbox) push(msg []byte) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return false
	}
	if o.size+int64(len(msg)) > o.max {
		o.closed, o.overflowed, o.msgs = true, true, nil
		o.signal()
		return false
	}
	o.msgs = append(o.msgs, msg)
	o.size += int64(len(msg))
	o.signal()
	return true
}

func (o *outbox) signal() {
	select {
	case o.ready <- struct{}{}:
	default:
	}
}

// take returns everything queued. closed reports that nothing more will come; overflowed
// that the client fell too far behind.
func (o *outbox) take() (msgs [][]byte, closed, overflowed bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	msgs, o.msgs, o.size = o.msgs, nil, 0
	return msgs, o.closed, o.overflowed
}

// close stops the outbox; what is queued can still be taken.
func (o *outbox) close() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.closed = true
	o.signal()
}
