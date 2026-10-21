package api

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/arumadev/aruma/internal/platform/httpx"
	"github.com/arumadev/aruma/internal/services/rtdb/core"
	"github.com/arumadev/aruma/internal/services/rtdb/wire"
)

// serveWS upgrades /.ws to the WebSocket protocol (A-protocol-sdks.md §1) and serves the
// connection until it ends. The connection is hijacked, so the entry point's 60-second
// deadlines are lifted here; the idle timeout and the write timeout of conn.go take over.
func (a *API) serveWS(w http.ResponseWriter, r *http.Request, target core.Target) {
	host := a.host(r)
	if !a.originAllowed(r, host) {
		writeError(w, http.StatusForbidden, "This origin may not connect to the database.")
		return
	}
	if !a.enter() {
		writeError(w, http.StatusServiceUnavailable, "The server is shutting down.")
		return
	}
	defer a.wg.Done()
	ip := httpx.ClientAddr(r.Context())
	if !ip.IsValid() {
		ip = httpx.ClientIP(r, a.opts.TrustedProxies)
	}
	if !a.perIP.acquire(ip) {
		writeError(w, http.StatusTooManyRequests, "Too many connections from this address.")
		return
	}
	defer a.perIP.release(ip)

	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Time{})
	_ = rc.SetWriteDeadline(time.Time{})
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// The Origin rule, if any, is applied above.
		InsecureSkipVerify: true,
		CompressionMode:    websocket.CompressionDisabled,
	})
	if err != nil {
		return // Accept has answered
	}
	ws.SetReadLimit(maxFrameBytes)
	if s := r.URL.Query().Get("s"); s != "" {
		a.joinLP(ws, s, host)
		return
	}
	// The open rules of development reach clients on this machine only, judged by the
	// connection's own peer, never by forwarded headers.
	sess, err := a.openSession(target, r)
	if err != nil {
		_ = ws.Close(websocket.StatusTryAgainLater, "database unavailable")
		return
	}
	c := newWSConn(a, ws, sess)
	if !a.track(c) {
		sess.core.Close()
		_ = ws.Close(websocket.StatusGoingAway, "")
		return
	}
	defer a.untrack(c)
	// The SDKs reconnect to the host the handshake names (h), so it is exactly the host and
	// port this client used (ADR-018; docs/research/rtdb/J-lp-trap.md).
	c.sess.send(wire.EncodeHello(wire.Hello{Timestamp: a.svc.Now().UnixMilli(), Host: host, Session: c.sess.id}))
	c.run()
}

// joinLP serves a WebSocket that comes to take over the long-poll session s, the session id
// of its hello (lp.go). The emulator answers an unknown session with a reset to the host and
// drops the socket (golden lp_upgrade.json); so does Aruma.
func (a *API) joinLP(ws *websocket.Conn, s, host string) {
	lp := a.lpOfSession(s)
	var c *wsConn
	if lp != nil {
		c = newWSConn(a, ws, lp.sess)
		c.lp = lp
	}
	if c == nil || !lp.join(c) {
		ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
		defer cancel()
		_ = ws.Write(ctx, websocket.MessageText, wire.EncodeReset(host))
		_ = ws.CloseNow()
		return
	}
	if !a.track(c) {
		lp.leave(c)
		_ = ws.Close(websocket.StatusGoingAway, "")
		return
	}
	defer a.untrack(c)
	c.writeNow(wire.EncodeSwitchAck())
	c.runSecondary()
}

// maxFrameBytes caps one WebSocket frame. The SDKs send at most 16384 UTF-16 code units per
// frame, at most 49152 bytes; the emulator accepted 64 KB in one frame and closed the
// connection at 100 KB (B-emulator-oracle.md §1.9).
const maxFrameBytes = 96 << 10

// originAllowed applies rtdb.allowed_origins. Firebase checks no Origin, and by default
// neither does Aruma: the protocol carries its credentials inside the messages and reads no
// cookie, so a foreign page cannot borrow a user's session. An operator who wants to narrow
// which pages connect lists their hosts; a request without Origin (Android, iOS, the Admin
// SDKs) and a page on the database's own host are always allowed.
func (a *API) originAllowed(r *http.Request, host string) bool {
	origin := r.Header.Get("Origin")
	if origin == "" || len(a.opts.AllowedOrigins) == 0 {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	if strings.EqualFold(u.Host, host) {
		return true
	}
	return slices.Contains(a.opts.AllowedOrigins, strings.ToLower(u.Hostname()))
}

// sessionID returns a random 32-character alphanumeric session id, as the emulator's.
func sessionID() string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 32)
	for i := 0; i < len(b); {
		var r [1]byte
		_, _ = rand.Read(r[:])
		if r[0] < 248 { // 248 = 4*62: no bias
			b[i] = alphabet[r[0]%62]
			i++
		}
	}
	return string(b)
}

// enter admits a new connection unless the API is shutting down.
func (a *API) enter() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.shuttingDown {
		return false
	}
	a.wg.Add(1)
	return true
}

// track registers a connection for Shutdown; false once shutdown has begun, so a connection
// accepted meanwhile is closed rather than missed.
func (a *API) track(c transport) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.shuttingDown {
		return false
	}
	a.conns[c] = struct{}{}
	return true
}

func (a *API) untrack(c transport) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.conns, c)
}

// Shutdown closes every connection with 1001 (going away), then runs each one's
// onDisconnect writes, as if the client had dropped (ADR-019). It sends no s control message:
// that tells the SDKs the server is gone for good, and they would not reconnect
// (PersistentConnection.ts:876-882 interrupts with server_kill; Android
// PersistentConnectionImpl.java:539-554), so every app would stay offline after a restart.
// A plain close makes them back off and reconnect. http.Server.Shutdown does not see
// hijacked connections, so the entry point calls this beside it. It waits for the
// connections until ctx ends.
func (a *API) Shutdown(ctx context.Context) error {
	a.mu.Lock()
	a.shuttingDown = true
	conns := make([]transport, 0, len(a.conns))
	for c := range a.conns {
		conns = append(conns, c)
	}
	a.mu.Unlock()
	for _, c := range conns {
		c.shutdown()
	}
	done := make(chan struct{})
	go func() { a.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return errors.Join(errors.New("rtdb: connections still open at shutdown"), ctx.Err())
	}
}
