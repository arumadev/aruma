package api

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/osama300/aurma-project/internal/platform/httpx"
	"github.com/osama300/aurma-project/internal/services/rtdb/core"
	"github.com/osama300/aurma-project/internal/services/rtdb/wire"
)

// Long polling at /.lp is the transport of a browser whose WebSocket failed once: the JS SDK
// remembers the failure in localStorage and from then on starts with long polling, then
// moves to a WebSocket as soon as one works, which clears the memory (ADR-020 §4,
// docs/research/rtdb/J-lp-trap.md). Without it such a browser never connects again.
//
// A long-poll connection is a protocol session the client reaches by id and password over a
// series of GET requests (wire/lp.go has the envelope). The server holds one request at a
// time until it has something to send; a newer request releases the one held. The client's
// messages ride in the URLs, and the session handles them in the client's order.
//
// The upgrade (Connection.ts:240-304, 353-360; golden lp_upgrade.json): once started, the
// client opens a WebSocket with s=<the session id of hello>, which joins this session. The
// server greets it with a; the client pings it until two pongs came back, then sends a on
// it and n on the long poll, and from then sends over the WebSocket. When n arrives, the
// server ends the long poll's part of the stream with n and the WebSocket carries the rest:
// the outbox gets n and a fence at once, the long poll sends up to the fence, the
// WebSocket everything after. What the client sent over the WebSocket before n arrived waits
// for it, so its messages keep their order across the two transports too.

// Variables, so that tests can shorten them.
var (
	// lpHold is how long a poll is held when there is nothing to send. The SDK sends a new
	// request after 25 seconds anyway, which releases the one held (BrowserPollConnection.ts
	// KEEPALIVE_REQUEST_INTERVAL). The emulator holds an idle poll until the session times
	// out (golden lp_poll.json); answering sooner keeps the request well inside the server's
	// 60-second write timeout and the idle limits of proxies.
	lpHold = 20 * time.Second
	// lpAbandon closes a connection whose client has sent nothing for this long while no
	// request was held: the page is gone without sending disconn. Its onDisconnect writes
	// run then. The emulator ends a silent session after about 100 seconds (lp_poll.json);
	// a background tab whose timers the browser throttles to once a minute stays within it.
	lpAbandon = 90 * time.Second
)

// lpState is where a long-poll connection is in its life.
type lpState int

const (
	lpPolling lpState = iota // carrying the session
	lpClosing                // sent n: the next answer tells the client to stop
	lpDone                   // finished; requests get a reset
)

// lpConn is one long-poll connection.
type lpConn struct {
	api  *API
	sess *session
	id   string // the client's id and password for the connection
	pw   string
	ip   netip.Addr // the client address it counts against

	ctx    context.Context // ends with the connection
	cancel context.CancelFunc

	// in orders what the client sends, over the long poll and over the WebSocket that joins.
	in       sync.Mutex
	rcv      wire.LPReceiver
	switched bool     // n arrived: the client sends over the WebSocket now
	backlog  [][]byte // what the WebSocket brought before n
	waiting  int      // its bytes, bounded like the outbox

	mu        sync.Mutex
	state     lpState
	batch     int64         // the number of the next batch
	serial    int64         // the highest ser a request carried
	held      chan struct{} // closed to release the request held, nil when none is
	idle      *time.Timer   // closes the connection when the client stops polling
	secondary *wsConn       // the WebSocket that joined, if any
	// handover is closed once the long poll has sent its last message; the WebSocket then
	// drains the outbox.
	handover chan struct{}
	finished bool
}

// serveLP serves a request to /.lp.
func (a *API) serveLP(w http.ResponseWriter, r *http.Request, target core.Target) {
	q := r.URL.Query()
	switch {
	case q.Get(wire.LPStart) == "t":
		a.lpStart(w, r, target, q)
	case q.Get(wire.LPDisconnectFrame) == "t":
		// The page the client keeps open; its unload sends disconn. ns rides along, as
		// Aruma needs it to find the database when the host does not name it.
		u := url.Values{wire.LPDisconnect: {"t"}, wire.LPID: {q.Get(wire.LPID)}, wire.LPPassword: {q.Get(wire.LPPassword)}}
		if ns := q.Get("ns"); ns != "" {
			u.Set("ns", ns)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(wire.EncodeLPDisconnectFrame("/.lp?" + u.Encode()))
	case q.Get(wire.LPDisconnect) == "t":
		if c := a.lpFind(q.Get(wire.LPID), q.Get(wire.LPPassword)); c != nil {
			c.close()
		}
		// What the emulator answers (golden lp_disconnect.json); the page is unloading.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!-- -->"))
	default:
		a.lpPoll(w, r, q)
	}
}

// lpStart opens a connection and answers with its id and password and hello.
func (a *API) lpStart(w http.ResponseWriter, r *http.Request, target core.Target, q url.Values) {
	cb := q.Get(wire.LPCallback)
	if cb != "" && !wire.ValidLPCallback(cb) {
		writeError(w, http.StatusBadRequest, "Invalid callback id.")
		return
	}
	host := a.host(r)
	if !a.originAllowed(r, host) {
		writeError(w, http.StatusForbidden, "This origin may not connect to the database.")
		return
	}
	if !a.enter() {
		writeError(w, http.StatusServiceUnavailable, "The server is shutting down.")
		return
	}
	ip := httpx.ClientAddr(r.Context())
	if !ip.IsValid() {
		ip = httpx.ClientIP(r, a.opts.TrustedProxies)
	}
	if !a.perIP.acquire(ip) {
		a.wg.Done()
		writeError(w, http.StatusTooManyRequests, "Too many connections from this address.")
		return
	}
	sess, err := a.openSession(target, r)
	if err != nil {
		a.perIP.release(ip)
		a.wg.Done()
		e := core.AsError(err)
		writeError(w, e.HTTP, e.Message)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &lpConn{api: a, sess: sess, pw: lpPassword(), ip: ip, ctx: ctx, cancel: cancel, batch: 1, handover: make(chan struct{})}
	if !a.lpAdd(c) {
		c.finish(true)
		writeError(w, http.StatusServiceUnavailable, "The server is shutting down.")
		return
	}
	c.mu.Lock()
	c.idle = time.AfterFunc(lpAbandon, c.close)
	c.mu.Unlock()
	hello := wire.EncodeHello(wire.Hello{Timestamp: a.svc.Now().UnixMilli(), Host: host, Session: sess.id})
	writeLP(w, wire.EncodeLPStart(cb, c.id, c.pw, hello))
}

// lpPoll serves a request of a started connection: it takes the messages in the URL, then
// holds the request until there is something to send.
func (a *API) lpPoll(w http.ResponseWriter, r *http.Request, q url.Values) {
	c := a.lpFind(q.Get(wire.LPID), q.Get(wire.LPPassword))
	if c == nil {
		// An unknown or finished connection: the emulator sends the client back to the host
		// to start again (golden lp_upgrade.json).
		writeLP(w, append(wire.EncodeLPBatch(0, [][]byte{wire.EncodeReset(a.host(r))}), wire.EncodeLPClose(-1)...))
		return
	}
	segs, err := wire.ParseLPSegments(q)
	if err == nil {
		err = c.receive(segs)
	}
	if err != nil {
		c.close()
		writeError(w, http.StatusBadRequest, "Invalid long-poll request.")
		return
	}
	ser, _ := strconv.ParseInt(q.Get(wire.LPSerial), 10, 64)
	if body := c.wait(r.Context(), ser); body != nil {
		writeLP(w, body)
	}
}

// receive hands the session the messages the segments complete, in order.
func (c *lpConn) receive(segs []wire.LPSegment) error {
	c.in.Lock()
	defer c.in.Unlock()
	for _, s := range segs {
		if err := c.rcv.Add(s); err != nil {
			return err
		}
	}
	for {
		c.rcv.Max = c.sess.messageLimit()
		msg, err := c.rcv.Next()
		switch {
		case errors.Is(err, wire.ErrMessageTooBig) || errors.Is(err, wire.ErrMalformed):
			// As on a WebSocket: the error frame, no reply, and the connection stays.
			c.sess.send(wire.EncodeError(errorFrame))
			continue
		case err != nil:
			return err
		case msg == nil:
			return nil
		}
		if in, err := wire.Parse(msg); err == nil && in.Control != nil && in.Control.Type == wire.ControlEndTransmission {
			c.switchOver()
			continue
		}
		c.sess.handle(c.ctx, msg)
	}
}

// switchOver acts on the client's n: the long poll's part of the stream ends here, and what
// the WebSocket brought meanwhile is handled now. Without a WebSocket to move to, n means
// nothing and is ignored.
func (c *lpConn) switchOver() {
	c.mu.Lock()
	ok := c.secondary != nil && c.state == lpPolling && !c.switched
	c.mu.Unlock()
	if !ok || !c.sess.out.pushLast(wire.EncodeEndTransmission()) {
		return
	}
	c.switched = true
	for _, m := range c.backlog {
		c.sess.handle(c.ctx, m)
	}
	c.backlog, c.waiting = nil, 0
}

// fromSecondary takes a message the joined WebSocket received. Before the switch only its
// pings are answered, on the WebSocket itself, since the stream still runs over the long
// poll; requests wait for n.
func (c *lpConn) fromSecondary(ws *wsConn, msg []byte) {
	c.in.Lock()
	defer c.in.Unlock()
	if c.switched {
		c.sess.handle(ws.ctx, msg)
		return
	}
	in, err := wire.Parse(msg)
	switch {
	case err == nil && in.Control != nil:
		if in.Control.Type == wire.ControlPing {
			ws.writeNow(wire.EncodePong())
		}
		// The client's a says it moved; n on the long poll does the switch.
	default:
		// The SDK sends n right after a, so little comes before it; a client that sends
		// more than the outbox would hold loses the WebSocket, and the long poll goes on.
		if c.waiting += len(msg); int64(c.waiting) > c.sess.out.max {
			ws.cancel()
			return
		}
		c.backlog = append(c.backlog, msg)
	}
}

// wait holds a request until there is something to send, a newer request arrives, lpHold
// passes or the connection ends, and returns its answer; nil when the client went away
// first. A request that arrives after one with a higher serial is answered at once: it was
// overtaken on the way, and the one held stays.
func (c *lpConn) wait(ctx context.Context, ser int64) []byte {
	release := make(chan struct{})
	c.mu.Lock()
	if ser < c.serial {
		c.mu.Unlock()
		return c.answer(release, true)
	}
	c.serial = ser
	if c.held != nil {
		close(c.held)
	}
	c.held = release
	c.idle.Stop()
	c.mu.Unlock()
	timer := time.NewTimer(lpHold)
	defer timer.Stop()
	force := false
	for {
		if body := c.answer(release, force); body != nil {
			return body
		}
		select {
		case <-c.sess.out.ready:
		case <-release:
			force = true
		case <-timer.C:
			force = true
		case <-c.ctx.Done():
			force = true
		case <-ctx.Done():
			c.mu.Lock()
			if c.held == release {
				c.held = nil
				c.idle.Reset(lpAbandon)
			}
			c.mu.Unlock()
			return nil
		}
	}
}

// answer builds the answer to the request that release belongs to: the next batch of
// messages, up to the fence when the session moves to the WebSocket, and the close command
// when the long poll ends. Without force it answers only when there is something to say.
func (c *lpConn) answer(release chan struct{}, force bool) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	var body []byte
	switch c.state {
	case lpDone:
		body = wire.EncodeLPClose(-1)
	case lpClosing:
		// The answer after n tells the client to stop, as the emulator's does.
		body = append(wire.EncodeLPBatch(c.batch, nil), wire.EncodeLPClose(c.batch)...)
		c.batch++
		c.state = lpDone
	default:
		msgs, fenced, closed, _ := c.sess.out.takeUntilFence()
		if len(msgs) == 0 && !fenced && !closed && !force {
			return nil
		}
		body = wire.EncodeLPBatch(c.batch, msgs)
		switch {
		case fenced:
			c.state = lpClosing
			close(c.handover)
		case closed:
			// The session ended (or the client fell too far behind): stop after this batch,
			// and the client reconnects.
			body = append(body, wire.EncodeLPClose(c.batch)...)
			c.state = lpDone
		}
		c.batch++
	}
	if c.held == release {
		c.held = nil
	}
	if c.state == lpDone {
		go c.finishLP()
	} else if c.held == nil {
		c.idle.Reset(lpAbandon)
	}
	return body
}

// finishLP ends the long poll once it has nothing more to say: the whole connection, unless
// the session moved to a WebSocket, which owns it from then.
func (c *lpConn) finishLP() {
	if c.moved() {
		c.finish(false)
		return
	}
	c.close()
}

// moved reports whether the long poll has handed its session over to the WebSocket.
func (c *lpConn) moved() bool {
	select {
	case <-c.handover:
		return true
	default:
		return false
	}
}

// close ends the connection. Before the move to a WebSocket that is the end of the session,
// whose onDisconnect writes run, and of a WebSocket that joined; after it only the long poll
// ends. The client's page sends disconn for the long poll when it drops it after the
// upgrade, so this must not end the session then.
func (c *lpConn) close() {
	c.mu.Lock()
	c.state = lpDone
	ws := c.secondary
	c.mu.Unlock()
	if c.moved() {
		c.finish(false)
		return
	}
	if ws != nil {
		ws.cancel()
	}
	c.finish(true)
}

// finish releases what the connection holds, once, and the session too when it still owns
// it.
func (c *lpConn) finish(endSession bool) {
	c.mu.Lock()
	if c.finished {
		c.mu.Unlock()
		return
	}
	c.finished = true
	if c.idle != nil {
		c.idle.Stop()
	}
	c.mu.Unlock()
	c.api.lpRemove(c)
	c.cancel()
	if endSession {
		c.sess.close()
	}
	c.api.perIP.release(c.ip)
	c.api.wg.Done()
}

// join attaches the WebSocket that came with s=<session id> to the connection whose session
// that is. False when there is none to join, or it already has one.
func (c *lpConn) join(ws *wsConn) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != lpPolling || c.secondary != nil {
		return false
	}
	c.secondary = ws
	return true
}

// leave detaches the WebSocket when it closes before the switch; the long poll carries on,
// as the client does when an upgrade fails. After the switch the WebSocket ends the session.
func (c *lpConn) leave(ws *wsConn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.secondary == ws && !c.moved() {
		c.secondary = nil
	}
}

// shutdown ends the connection: the request held is told to close, and the client
// reconnects. After the move the WebSocket is shut down on its own.
func (c *lpConn) shutdown() { c.close() }

// writeLP sends a script answer.
func writeLP(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", wire.LPContentType)
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(body)
}

// lpPassword returns a random password for a long-poll connection. The emulator's are 10
// characters; these are longer, as nothing in the SDK reads them.
func lpPassword() string { return sessionID()[:20] }

// lpAdd registers a connection under a new id and its session id; false once shutdown has
// begun.
func (a *API) lpAdd(c *lpConn) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.shuttingDown {
		return false
	}
	a.lpSeq++
	c.id = strconv.FormatInt(a.lpSeq, 10)
	a.lps[c.id] = c
	a.lpSessions[c.sess.id] = c
	a.conns[c] = struct{}{}
	return true
}

func (a *API) lpRemove(c *lpConn) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.lps[c.id] == c {
		delete(a.lps, c.id)
	}
	if a.lpSessions[c.sess.id] == c {
		delete(a.lpSessions, c.sess.id)
	}
	delete(a.conns, c)
}

// lpFind returns the connection with id whose password is pw, or nil.
func (a *API) lpFind(id, pw string) *lpConn {
	a.mu.Lock()
	defer a.mu.Unlock()
	c := a.lps[id]
	if c == nil || subtle.ConstantTimeCompare([]byte(c.pw), []byte(pw)) != 1 {
		return nil
	}
	return c
}

// lpOfSession returns the long-poll connection carrying session id, or nil.
func (a *API) lpOfSession(id string) *lpConn {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lpSessions[id]
}
