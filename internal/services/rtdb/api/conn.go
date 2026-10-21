package api

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/arumadev/aruma/internal/services/rtdb/wire"
)

const (
	// idleTimeout closes a connection the client has been silent on. The SDKs send a "0"
	// frame after 45 seconds of silence; the emulator closes at about 120 seconds.
	idleTimeout = 120 * time.Second
	// writeTimeout bounds one frame's write; a client that stops reading is dropped.
	writeTimeout = 30 * time.Second
)

// wsConn carries one session over a WebSocket: a reader that reassembles frames into
// messages for the session, and a writer that drains the session's outbox into frames.
type wsConn struct {
	api  *API
	ws   *websocket.Conn
	sess *session
	// lp is the long-poll connection this WebSocket joined, for an upgrade (lp.go); nil for
	// a WebSocket that started its own session.
	lp *lpConn

	ctx    context.Context // ends both loops
	cancel context.CancelFunc
	once   sync.Once
}

func newWSConn(a *API, ws *websocket.Conn, s *session) *wsConn {
	ctx, cancel := context.WithCancel(context.Background())
	return &wsConn{api: a, ws: ws, sess: s, ctx: ctx, cancel: cancel}
}

// run serves the connection until either side ends it, then closes the session, which runs
// its onDisconnect writes.
func (c *wsConn) run() {
	written := make(chan struct{})
	go func() {
		defer close(written)
		c.writeLoop()
	}()
	c.readLoop()
	c.cancel()
	<-written
	c.sess.close()
	_ = c.ws.CloseNow()
}

func (c *wsConn) readLoop() {
	asm := wire.Assembler{Budget: c.api.inflight}
	defer asm.Reset()
	for {
		ctx, cancel := context.WithTimeout(c.ctx, idleTimeout)
		typ, frame, err := c.ws.Read(ctx)
		cancel()
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			continue
		}
		if !asm.Pending() {
			// A session that has not signed in gets a smaller limit, so anonymous sockets
			// cannot make the server hold large messages.
			asm.Max = c.sess.messageLimit()
		}
		msg, err := asm.Feed(frame)
		switch {
		case errors.Is(err, wire.ErrMessageTooBig):
			// As the emulator does, an oversized message (or one the shared budget for
			// partly received messages cannot hold) is dropped with the error frame, and
			// the connection stays open; the request gets no reply. Kept on purpose: no SDK
			// sends a write over the limits it checks itself.
			c.sess.send(wire.EncodeError(errorFrame))
			continue
		case msg == nil:
			continue
		}
		if c.lp != nil {
			c.lp.fromSecondary(c, msg)
			continue
		}
		c.sess.handle(c.ctx, msg)
	}
}

// runSecondary serves a WebSocket that joined a long-poll session. It drains the outbox only
// once the long poll has sent its last message; until then it answers pings itself. If it
// closes before that, the long poll carries on; after, the session ends with it.
func (c *wsConn) runSecondary() {
	written := make(chan struct{})
	go func() {
		defer close(written)
		select {
		case <-c.lp.handover:
			c.writeLoop()
		case <-c.ctx.Done():
		}
	}()
	c.readLoop()
	c.cancel()
	<-written
	c.lp.leave(c)
	if c.lp.moved() {
		c.sess.close()
	}
	_ = c.ws.CloseNow()
}

// writeNow writes one message at once, beside the outbox: the frames of an upgrade that
// belong to this WebSocket alone.
func (c *wsConn) writeNow(msg []byte) {
	ctx, cancel := context.WithTimeout(c.ctx, writeTimeout)
	defer cancel()
	if err := c.ws.Write(ctx, websocket.MessageText, msg); err != nil {
		c.cancel()
	}
}

func (c *wsConn) writeLoop() {
	out := c.sess.out
	for {
		select {
		case <-out.ready:
		case <-c.ctx.Done():
			return
		}
		msgs, closed, overflowed := out.take()
		for _, m := range msgs {
			for _, f := range wire.Split(m) {
				ctx, cancel := context.WithTimeout(c.ctx, writeTimeout)
				err := c.ws.Write(ctx, websocket.MessageText, f)
				cancel()
				if err != nil {
					c.cancel()
					return
				}
			}
		}
		switch {
		case overflowed:
			_ = c.ws.Close(websocket.StatusPolicyViolation, "client too slow")
			c.cancel()
			return
		case closed:
			_ = c.ws.Close(websocket.StatusGoingAway, "")
			c.cancel()
			return
		}
	}
}

// shutdown closes the connection with 1001 once what is queued is sent, without the s
// control message, so the client reconnects (see API.Shutdown). The session then closes as
// on any disconnection.
func (c *wsConn) shutdown() {
	c.once.Do(c.sess.out.close)
}
