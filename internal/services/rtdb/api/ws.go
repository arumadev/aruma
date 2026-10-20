package api

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/osama300/aurma-project/internal/services/rtdb/core"
	"github.com/osama300/aurma-project/internal/services/rtdb/wire"
)

// serveWS upgrades /.ws to the WebSocket protocol (A-protocol-sdks.md §1) and serves the
// connection until it ends. The connection is hijacked, so the entry point's 60-second
// deadlines are lifted here; the idle timeout and the write timeout of conn.go take over.
func (a *API) serveWS(w http.ResponseWriter, r *http.Request, target core.Target) {
	if !a.originAllowed(r, target) {
		writeError(w, http.StatusForbidden, "This origin may not connect to the database.")
		return
	}
	if !a.enter() {
		writeError(w, http.StatusServiceUnavailable, "The server is shutting down.")
		return
	}
	defer a.wg.Done()
	release, err := a.svc.Connect(target)
	if err != nil {
		writeError(w, http.StatusTooManyRequests, "Too many connections to this project's database.")
		return
	}
	defer release()

	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Time{})
	_ = rc.SetWriteDeadline(time.Time{})
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// The Origin rule is applied above, with the project's authorised domains.
		InsecureSkipVerify: true,
		CompressionMode:    websocket.CompressionDisabled,
	})
	if err != nil {
		return // Accept has answered
	}
	ws.SetReadLimit(maxFrameBytes)
	cs, err := a.svc.Open(target, core.Auth{})
	if err != nil {
		_ = ws.Close(websocket.StatusTryAgainLater, "database unavailable")
		return
	}
	c := newWSConn(a, ws, newSession(a, target, cs, a.svc.Limits().OutboxBytes))
	// The SDKs reconnect to the host the handshake names (h), so it is exactly the host and
	// port this client used (ADR-018; docs/research/rtdb/J-lp-trap.md).
	c.sess.send(wire.EncodeHello(wire.Hello{Timestamp: a.svc.Now().UnixMilli(), Host: r.Host, Session: c.sess.id}))
	a.track(c)
	defer a.untrack(c)
	c.run()
}

// maxFrameBytes caps one WebSocket frame. The SDKs send at most 16384 UTF-16 code units per
// frame, at most 49152 bytes; the emulator accepted 64 KB in one frame and closed the
// connection at 100 KB (B-emulator-oracle.md §1.9).
const maxFrameBytes = 96 << 10

// originAllowed applies the Origin rule (rtdb-plan.md, D-articles-design.md): no Origin, as
// from Android, iOS and the Admin SDKs, is allowed; a browser's Origin must be the database's
// own host or one of the project's authorised domains. Tokens travel inside the messages and
// no cookie is read, so the rule narrows which pages may use the database, nothing more.
// Firebase itself checks no Origin.
func (a *API) originAllowed(r *http.Request, t core.Target) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	if a.opts.AllowedOrigins == nil {
		return false
	}
	hosts, err := a.opts.AllowedOrigins(r.Context(), t.Project)
	if err != nil {
		a.log.Warn("rtdb: authorised domains", "project", t.Project, "error", err)
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, h := range hosts {
		if strings.EqualFold(h, host) {
			return true
		}
	}
	return false
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

func (a *API) track(c *wsConn) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.conns[c] = struct{}{}
}

func (a *API) untrack(c *wsConn) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.conns, c)
}

// Shutdown closes every connection: each client is told the server is going away (the s
// control message, after which the SDKs report it and reconnect later), then its
// onDisconnect writes run, as if it had dropped (ADR-019). http.Server.Shutdown does not see
// hijacked connections, so the entry point calls this beside it. It waits for the
// connections until ctx ends.
func (a *API) Shutdown(ctx context.Context) error {
	a.mu.Lock()
	a.shuttingDown = true
	conns := make([]*wsConn, 0, len(a.conns))
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
