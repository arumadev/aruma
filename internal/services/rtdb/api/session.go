package api

import (
	"net/http"
	"sync"

	"github.com/osama300/aurma-project/internal/platform/httpx"

	"github.com/osama300/aurma-project/internal/services/rtdb/core"
	"github.com/osama300/aurma-project/internal/services/rtdb/wire"
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
	closed  bool
	admin   adminState // the admin credential the session signed in with (auth.go)
}

// openSession starts a protocol session and its core session, which pushes into it. The
// open rules of development reach clients on this machine only, judged by the connection's
// own peer, never by forwarded headers.
func (a *API) openSession(target core.Target, r *http.Request) (*session, error) {
	s := &session{api: a, target: target, out: newOutbox(a.svc.Limits().OutboxBytes), id: sessionID()}
	cs, err := a.svc.Open(target, core.Auth{}, core.OpenOptions{Loopback: httpx.PeerIsLoopback(r), Sink: s})
	if err != nil {
		return nil, err
	}
	s.core = cs
	return s, nil
}

// messageLimit is the largest message the session may send now: the full limit once it has
// signed in, a small one before (rtdb.limits.unauthenticated_message_bytes).
func (s *session) messageLimit() int {
	lim := s.api.svc.Limits()
	if s.core.Authenticated() {
		return int(lim.MessageBytes)
	}
	return int(lim.UnauthenticatedMessageBytes)
}

// slot holds the reply to one request until every earlier reply is out. Requests the writer
// runs complete later than those answered on the spot, but the client receives the replies in
// the order it asked, as from Firebase (pipelined requests are answered in order,
// B-emulator-oracle.md §1.3). Pushes do not wait: a write's push reaches the outbox before
// its ack, since the writer publishes before it replies.
type slot struct {
	id   int64
	msg  []byte
	done bool
}

// reserve takes the next reply slot, for request id.
func (s *session) reserve(id int64) *slot {
	s.mu.Lock()
	defer s.mu.Unlock()
	sl := &slot{id: id}
	s.pending = append(s.pending, sl)
	return sl
}

// fill completes a slot and sends every reply that is now first in line. A reply larger than
// the whole outbox could never be sent: it becomes a too_big error, which the SDKs know for
// a read too large to answer, and the connection stays (review #9; a backlog of smaller
// messages still closes it).
func (s *session) fill(sl *slot, msg []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if int64(len(msg)) > s.out.max {
		msg = wire.EncodeResponse(sl.id, wire.Fail(wire.StatusTooBig,
			"Data requested exceeds the maximum size that can be accessed with a single request."))
	}
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
	s.mu.Lock()
	s.closed = true
	s.admin.stop()
	s.mu.Unlock()
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
