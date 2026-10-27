// Package webchannel is the Document Database's WebChannel surface (ADR-027 §5): the protocol the
// full web SDK speaks for Listen and Write, at /google.firestore.v1.Firestore/{Listen,Write}/channel,
// on top of the same streams as gRPC (grpc.Handler.ServeListen and ServeWrite), so a stream has the same
// semantics, limits, identity and rules on both.
//
// A session is opened by a handshake POST, which carries the client's headers in its body
// (headers=, encodeInitMessageHeaders) and usually the stream's first request; the server answers
// with the session's id (SID) and its X-HTTP-Session-Id, which the client sends back as
// gsessionid. The client then sends requests with forward channel POSTs, and reads the
// responses on a backchannel GET (TYPE=xmlhttp), held open and streamed (CI=0), or answered as
// soon as there is something to send (CI=1, long polling); keep-alives (noop) keep it alive, and
// TYPE=terminate ends the session. The identity is the headers' token or API key, never a
// cookie: no request of the surface reads one.
//
// Every bound a stream has on gRPC holds (ADR-026 §3); since opening a session costs a client
// one request, sessions are bounded too: per client address, per project as the handshake names
// it, and in all; the bytes a session holds for its client; and its idle life without a
// backchannel.
package webchannel

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/platform/httpx"
	"github.com/arumadev/aruma/internal/services/documents/api"
	"github.com/arumadev/aruma/internal/services/documents/grpc"
	"github.com/arumadev/aruma/internal/services/documents/wire"
	tcore "github.com/arumadev/aruma/internal/tenancy/core"
)

// ListenPath and WritePath are the channel paths (rest.ChannelPaths).
const (
	ListenPath = "/google.firestore.v1.Firestore/Listen/channel"
	WritePath  = "/google.firestore.v1.Firestore/Write/channel"
)

// Streams runs the streams: *grpc.Handler.
type Streams interface {
	ServeListen(ctx context.Context, p grpc.Peer, c api.Call, conn grpc.ListenConn) *wire.Error
	ServeWrite(ctx context.Context, p grpc.Peer, c api.Call, conn grpc.WriteConn) *wire.Error
}

// Keys resolves a client API key (ADR-027 §5.3): *tenancy/core.Service.
type Keys interface {
	ProjectForKey(ctx context.Context, key string) (tcore.KeyProject, error)
}

// Limits are the surface's bounds; a zero field takes its default.
type Limits struct {
	// MessageBytes caps a request's body, and the requests a session holds unread
	// (documents.limits.message_bytes, 17 MiB).
	MessageBytes int64
	// SessionBytes caps the arrays a session holds unacknowledged
	// (documents.limits.stream_outbox_bytes, 64 MiB).
	SessionBytes int64
	// SessionsPerIP caps the sessions of one client address (streams_per_ip, 500).
	SessionsPerIP int
	// SessionsPerProject caps the sessions naming one project (streams_per_project, 10000).
	SessionsPerProject int
	// Sessions caps the sessions in all (100000).
	Sessions int
	// WriteStall bounds a write to a backchannel, and a forward POST waiting for the stream to
	// read (stream_write_stall, 30 s).
	WriteStall time.Duration
}

// Timing are the protocol's intervals; a zero field takes its default. None is production's
// recorded value yet unless it says so (ADR-027 §5.5).
type Timing struct {
	// Noop is the keep-alive interval of a streamed backchannel (30 s, production's, the
	// handshake's 30000): below the client's 45-second watchdog of a request that receives
	// nothing (ChannelRequest.TIMEOUT_MS_).
	Noop time.Duration
	// LongPoll is how long a long poll (CI=1) is held without anything to send (29 s; production
	// ended an idle one after 29.5 s), or the client's TO when shorter.
	LongPoll time.Duration
	// Backchannel is the longest a streamed backchannel is held (48 s; production's ended after
	// 48 to 52 s): the client opens the next one, which acknowledges what this one carried.
	Backchannel time.Duration
	// Expiry ends a session without a backchannel and without a request for this long (60 s).
	Expiry time.Duration
}

// Options configure the handler.
type Options struct {
	Streams Streams
	// Keys, when set, resolves the API key a handshake carries; without it the key is ignored.
	Keys   Keys
	Limits Limits
	Timing Timing
	Logger *slog.Logger
}

// Handler serves the channel paths. It is safe for concurrent use.
type Handler struct {
	streams Streams
	keys    Keys
	limits  Limits
	timing  Timing
	log     *slog.Logger

	mu        sync.Mutex
	sessions  map[string]*session
	byIP      map[netip.Addr]int
	byProject map[string]int
}

// New returns the handler.
func New(o Options) *Handler {
	l := o.Limits
	l.MessageBytes = cmp.Or(l.MessageBytes, 17<<20)
	l.SessionBytes = cmp.Or(l.SessionBytes, 64<<20)
	l.SessionsPerIP = cmp.Or(l.SessionsPerIP, 500)
	l.SessionsPerProject = cmp.Or(l.SessionsPerProject, 10_000)
	l.Sessions = cmp.Or(l.Sessions, 100_000)
	l.WriteStall = cmp.Or(l.WriteStall, 30*time.Second)
	t := o.Timing
	t.Noop = cmp.Or(t.Noop, 30*time.Second)
	t.LongPoll = cmp.Or(t.LongPoll, 29*time.Second)
	t.Backchannel = cmp.Or(t.Backchannel, 48*time.Second)
	t.Expiry = cmp.Or(t.Expiry, time.Minute)
	return &Handler{
		streams: o.Streams, keys: o.Keys, limits: l, timing: t, log: cmp.Or(o.Logger, slog.Default()),
		sessions: map[string]*session{}, byIP: map[netip.Addr]int{}, byProject: map[string]int{},
	}
}

// maxMaps is the most maps a forward request carries (WebChannelBase.MAX_MAPS_PER_REQUEST_).
const maxMaps = 1000

// ServeHTTP serves a request of a channel path. CORS is the mount's (rest.cors).
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	switch {
	case q.Get("TYPE") == "terminate":
		h.terminate(w, q)
	case !q.Has("VER"):
		// Production answers a request without a version, a probe, with an empty 200 (and a
		// cookie, which no request of the surface sets) (f6-webchannel-errors.json).
		hd := w.Header()
		hd.Set("Cache-Control", "private")
		hd.Set("Content-Type", contentType)
		hd.Set("Content-Length", "0")
		if r.Header.Get("Origin") != "" {
			hd.Set("Access-Control-Expose-Headers", "x-client-wire-protocol")
		}
		w.WriteHeader(http.StatusOK)
	case q.Get("VER") != "8":
		fail(w, http.StatusBadRequest, "Unsupported WebChannel version.")
	case r.Method == http.MethodGet && q.Get("RID") == "rpc":
		h.backchannel(w, r, q)
	case r.Method == http.MethodPost && !q.Has("SID"):
		h.handshake(w, r, q)
	case r.Method == http.MethodPost:
		h.forward(w, r, q)
	default:
		fail(w, http.StatusBadRequest, "Bad request.")
	}
}

// fail answers a request the protocol refuses, in Google's JSON error form. A text naming an
// unknown session must not start the body: the client looks for "Unknown SID" past index 0
// (ChannelRequest.onXmlHttpReadyStateChanged_).
func fail(w http.ResponseWriter, status int, text string) {
	code := wire.CodeInvalidArgument
	switch status {
	case http.StatusForbidden:
		code = wire.CodePermissionDenied
	case http.StatusRequestEntityTooLarge:
		code = wire.CodeInvalidArgument
	case http.StatusServiceUnavailable:
		code = wire.CodeUnavailable
	}
	body, _ := json.Marshal(map[string]any{"error": map[string]any{"code": status, "message": text, "status": code.String()}})
	hd := w.Header()
	hd.Set("Content-Type", wire.RESTContentType)
	hd.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

const errUnknownSID = "Unknown SID"

// unknownSID answers a request naming no session: 400 and an HTML page, as production's
// (f6-webchannel-errors.json); the client looks for "Unknown SID" past index 0.
func unknownSID(w http.ResponseWriter) {
	body := "<!DOCTYPE html><html lang=en><title>Error 400 (Bad Request)</title>" +
		"<p><b>400.</b> <ins>That's an error.</ins><p>Your client has issued a malformed or illegal request. Unknown SID</html>"
	hd := w.Header()
	hd.Set("Content-Type", "text/html; charset=UTF-8")
	hd.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusBadRequest)
	_, _ = io.WriteString(w, body)
}

// writeChunks answers a request with chunks of the given texts. A handshake's and a forward
// POST's answers end with a newline in production, a backchannel's records do not.
func writeChunks(w http.ResponseWriter, texts ...[]byte) {
	var b []byte
	for _, t := range texts {
		b = chunk(b, t)
	}
	b = append(b, '\n')
	hd := w.Header()
	hd.Set("Content-Type", contentType)
	hd.Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

// contentType is the chunks' Content-Type.
const contentType = "text/plain; charset=utf-8"

// readBody reads a forward body: the mount caps it at message_bytes.
func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		fail(w, http.StatusRequestEntityTooLarge, "The request is too large.")
		return nil, false
	}
	return body, true
}

// randomID is a session's id: n random bytes in base64url, unguessable. Production's SID is 22
// characters (16 bytes), its gsessionid about 54 (40 bytes).
func randomID(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// handshake opens a session (WebChannelBase.open_): it answers at once with the session's id,
// and starts its stream with the requests the body carries.
func (h *Handler) handshake(w http.ResponseWriter, r *http.Request, q url.Values) {
	if _, err := strconv.Atoi(q.Get("RID")); err != nil {
		fail(w, http.StatusBadRequest, "Bad request.")
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	f, err := parseForward(body, maxMaps)
	if err != nil {
		fail(w, http.StatusBadRequest, "Bad request.")
		return
	}
	client := httpx.ClientAddr(r.Context())
	database := q.Get("database")
	project := projectOf(database)
	headers := f.headers
	if headers == nil {
		headers = http.Header{}
	}
	if !h.checkKey(w, r, cmp.Or(headers.Get("X-Goog-Api-Key"), q.Get("key")), project, client) {
		return
	}

	s := &session{h: h, id: randomID(16), http: randomID(40), client: client, project: project,
		changed: make(chan struct{}), lastMap: -1, nextID: 1, active: time.Now()}
	// The stream outlives the request: its context is the session's.
	ctx := context.WithoutCancel(r.Context())
	s.ctx, s.cancel = context.WithCancelCause(ctx)
	hello := handshakeArray(s.id)
	s.expiry = time.AfterFunc(h.timing.Expiry, s.checkExpiry)
	if refused := h.add(s); refused != nil {
		// Refused at its opening, as gRPC refuses the stream: the status follows the handshake
		// in the same answer, and the session is never kept.
		s.expiry.Stop()
		s.cancel(nil)
		writeHandshake(w, s, hello, closing(refused.RESTBody())...)
		return
	}
	_ = s.take(f.maps, h.limits.WriteStall)

	c := api.Call{
		Authorization: headers.Get("Authorization"),
		Transport:     authn.TransportOf(r),
		// The SDK names the database in the URL (messageUrlParams), as gRPC names it in
		// x-goog-request-params; a stream whose first request names another is refused alike.
		ResourcePrefix: headers.Get("Google-Cloud-Resource-Prefix"),
		RequestParams:  cmp.Or(headers.Get("X-Goog-Request-Params"), "database="+url.QueryEscape(database)),
	}
	peer := grpc.Peer{Procedure: r.URL.Path, Client: client, Transport: c.Transport, Deadlines: s}
	go func() {
		var status *wire.Error
		if r.URL.Path == WritePath {
			status = h.streams.ServeWrite(s.ctx, peer, c, writeConn{s})
		} else {
			status = h.streams.ServeListen(s.ctx, peer, c, listenConn{s})
		}
		s.end(status)
	}()
	writeHandshake(w, s, hello)
}

// handshakeArray is the handshake's payload: the session's id, no host prefix, the channel
// version, the server's version, and the server's keep-alive interval (WebChannelBase.onInput_),
// as production answers it: ["c",SID,"",8,15,30000] (f6-webchannel-listen.json).
func handshakeArray(sid string) []byte {
	b, _ := json.Marshal([]any{"c", sid, "", 8, 15, 30000})
	return b
}

func writeHandshake(w http.ResponseWriter, s *session, hello []byte, more ...[]byte) {
	as := []array{{id: 0, payload: hello}}
	for i, m := range more {
		as = append(as, array{id: i + 1, payload: m})
	}
	hd := w.Header()
	hd.Set("X-HTTP-Session-Id", s.http)
	// Production exposes x-client-wire-protocol too, which this server does not send (ADR-027
	// §5.5).
	hd.Set("Access-Control-Expose-Headers", "x-client-wire-protocol,x-http-session-id")
	writeChunks(w, arrays(as))
}

// projectOf is the project id of projects/{p}/databases/{d}, or "".
func projectOf(database string) string {
	rest, ok := strings.CutPrefix(database, "projects/")
	if !ok {
		return ""
	}
	p, _, _ := strings.Cut(rest, "/")
	return p
}

// Errors of the session bounds, sent in the handshake's answer; our texts.
var (
	errTooManyIP = wire.NewError(wire.CodeResourceExhausted,
		"Too many streams are open from this address.")
	errTooManyProject = wire.NewError(wire.CodeResourceExhausted,
		"Too many streams are open on this project.")
	errTooMany = wire.NewError(wire.CodeResourceExhausted,
		"Too many streams are open on this server.")
)

// add registers a session within the bounds, or returns the refusal.
func (h *Handler) add(s *session) *wire.Error {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case len(h.sessions) >= h.limits.Sessions:
		h.log.Warn("documents: stream limit reached", "limit", "webchannel_sessions", "client_ip", s.client.String())
		return errTooMany
	case h.byIP[s.client] >= h.limits.SessionsPerIP:
		h.log.Warn("documents: stream limit reached", "limit", "streams_per_ip", "client_ip", s.client.String())
		return errTooManyIP
	case h.byProject[s.project] >= h.limits.SessionsPerProject:
		h.log.Warn("documents: stream limit reached", "limit", "streams_per_project", "client_ip", s.client.String())
		return errTooManyProject
	}
	h.sessions[s.id] = s
	h.byIP[s.client]++
	h.byProject[s.project]++
	return nil
}

// remove ends a session: its stream ends, and its requests are answered Unknown SID from now on.
func (h *Handler) remove(s *session, cause error) {
	h.mu.Lock()
	if h.sessions[s.id] == s {
		delete(h.sessions, s.id)
		if h.byIP[s.client]--; h.byIP[s.client] <= 0 {
			delete(h.byIP, s.client)
		}
		if h.byProject[s.project]--; h.byProject[s.project] <= 0 {
			delete(h.byProject, s.project)
		}
	}
	h.mu.Unlock()
	s.cancel(cause)
	s.expiry.Stop()
	s.mu.Lock()
	s.removed = true
	s.wakeLocked()
	s.mu.Unlock()
}

// Sessions is the number of sessions open.
func (h *Handler) Sessions() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.sessions)
}

// lookup finds the session a request names by SID and gsessionid, and marks it active.
func (h *Handler) lookup(q url.Values) *session {
	h.mu.Lock()
	s := h.sessions[q.Get("SID")]
	h.mu.Unlock()
	if s == nil || q.Get("gsessionid") != s.http {
		return nil
	}
	s.mu.Lock()
	s.touchLocked()
	if aid, err := strconv.Atoi(q.Get("AID")); err == nil {
		s.ackLocked(aid)
	}
	s.mu.Unlock()
	return s
}

// forward takes a forward channel POST's requests, and answers with the backchannel's state
// (WebChannelBase.handlePostResponse_): whether there is one, the last array id sent, and the
// bytes not acknowledged. Production answers 1 and 7 there in every recorded state, streamed or
// long polled, all arrays acknowledged or not (f6-webchannel-*.json): a backchannel counts as
// there once one was attached, a long poll's being between two GETs most of the time, and the
// bytes are production's 7, below the 37500 under which the client watches a buffered
// backchannel as it would for 0.
func (h *Handler) forward(w http.ResponseWriter, r *http.Request, q url.Values) {
	s := h.lookup(q)
	if s == nil {
		unknownSID(w)
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	f, err := parseForward(body, maxMaps)
	if err != nil {
		fail(w, http.StatusBadRequest, "Bad request.")
		return
	}
	if err := s.take(f.maps, h.limits.WriteStall); err != nil {
		if err == errBusy {
			fail(w, http.StatusServiceUnavailable, "The stream is not reading its requests.")
		} else {
			unknownSID(w)
		}
		return
	}
	s.mu.Lock()
	present := 0
	if s.back != nil || s.backed {
		present = 1
	}
	state, _ := json.Marshal([]any{present, s.nextID - 1, 7})
	s.mu.Unlock()
	writeChunks(w, state)
}

// terminate ends a session (WebChannelBase.disconnect): sent by navigator.sendBeacon, an image or
// an XHR, so by POST or GET, and answered with nothing; an unknown session is Unknown SID, as
// production answers it (f6-webchannel-errors.json).
func (h *Handler) terminate(w http.ResponseWriter, q url.Values) {
	s := h.lookup(q)
	if s == nil {
		unknownSID(w)
		return
	}
	h.remove(s, errTerminated)
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(http.StatusOK)
}

// listenConn and writeConn are a stream's messages on a session: requests and responses in
// proto3 JSON, as the SDK sends and reads them.
type (
	listenConn struct{ s *session }
	writeConn  struct{ s *session }
)
