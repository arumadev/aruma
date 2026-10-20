package api

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/osama300/aurma-project/internal/services/rtdb/wire"
)

const (
	// idleTimeout closes a connection the client has been silent on. The SDKs send a "0"
	// frame after 45 seconds of silence; the emulator closes at about 120 seconds.
	idleTimeout = 120 * time.Second
	// writeTimeout bounds one frame's write; a client that stops reading is dropped.
	writeTimeout = 30 * time.Second
	// shutdownReason is the s control message's text.
	shutdownReason = "The server is shutting down."
)

// wsConn carries one session over a WebSocket: a reader that reassembles frames into
// messages for the session, and a writer that drains the session's outbox into frames.
type wsConn struct {
	api  *API
	ws   *websocket.Conn
	sess *session

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
	asm := wire.Assembler{Max: int(c.api.svc.Limits().MessageBytes)}
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
		msg, err := asm.Feed(frame)
		switch {
		case errors.Is(err, wire.ErrMessageTooBig):
			// The emulator answers an oversized message with its error frame and drops it;
			// the connection stays open.
			c.sess.send(wire.EncodeError(errorFrame))
			continue
		case msg == nil:
			continue
		}
		c.sess.handle(c.ctx, msg)
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

// shutdown tells the client the server is going away and closes the connection once that is
// sent. The session then closes as on any disconnection.
func (c *wsConn) shutdown() {
	c.once.Do(func() {
		c.sess.send(wire.EncodeShutdown(shutdownReason))
		c.sess.out.close()
	})
}
