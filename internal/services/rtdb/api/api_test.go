package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/osama300/aurma-project/internal/platform/apierr"
	"github.com/osama300/aurma-project/internal/platform/authn"
	"github.com/osama300/aurma-project/internal/services/rtdb/core"
	"github.com/osama300/aurma-project/internal/services/rtdb/wire"
	tcore "github.com/osama300/aurma-project/internal/tenancy/core"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

// memStore keeps databases in memory, on core.Node.Set.
type memStore struct {
	mu    sync.Mutex
	roots map[string]*core.Node
	seq   map[string]int64
	rules map[string]string
}

func (s *memStore) OpenDatabase(_ context.Context, project int64, name string) (core.DatabaseInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rules := s.rules[name]
	if rules == "" {
		rules = core.LockedRules
	}
	return core.DatabaseInfo{ID: 1, Project: project, Name: name, Rules: rules}, nil
}

func (s *memStore) Begin(_ context.Context, db core.DatabaseInfo) (core.StoreTx, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return &memTx{s: s, name: db.Name, root: s.roots[db.Name], seq: s.seq[db.Name]}, nil
}

func (s *memStore) Seq(_ context.Context, db core.DatabaseInfo) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seq[db.Name], nil
}

func (s *memStore) DeleteProject(context.Context, int64) error { return nil }

func (s *memStore) SetRules(_ context.Context, _ int64, name, doc string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rules == nil {
		s.rules = map[string]string{}
	}
	s.rules[name] = doc
	return nil
}

func (s *memStore) value(name string) *core.Node {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.roots[name]
}

type memTx struct {
	s    *memStore
	name string
	root *core.Node
	seq  int64
}

func (t *memTx) Read(_ context.Context, p core.Path) (*core.Node, error) { return t.root.Get(p), nil }
func (t *memTx) Peek(_ context.Context, p core.Path) (*core.Node, bool, error) {
	n := t.root.Get(p)
	if n.IsLeaf() {
		return n, false, nil
	}
	return nil, n != nil, nil
}
func (t *memTx) HasChildExcept(_ context.Context, p core.Path, keys []string) (bool, error) {
	for _, k := range t.root.Get(p).Keys() {
		if !slices.Contains(keys, k) {
			return true, nil
		}
	}
	return false, nil
}
func (t *memTx) Write(_ context.Context, p core.Path, v *core.Node) error {
	t.root = t.root.Set(p, v)
	return nil
}
func (t *memTx) AdvanceSeq(_ context.Context, n int64) (int64, error) { t.seq += n; return t.seq, nil }
func (t *memTx) Rollback(context.Context) error                       { return nil }
func (t *memTx) Commit(context.Context) error {
	t.s.mu.Lock()
	defer t.s.mu.Unlock()
	t.s.roots[t.name], t.s.seq[t.name] = t.root, t.seq
	return nil
}

type projects map[string]int64

func (p projects) ProjectNumber(_ context.Context, id string) (int64, error) {
	if id == "outage" {
		return 0, errors.New("connection refused")
	}
	if n, ok := p[id]; ok {
		return n, nil
	}
	return 0, apierr.New(apierr.NotFound, "Requested entity was not found.")
}

// verifier stands in for the control plane's credentials: service tokens of project
// 482915736204 that it can revoke, each with an expiry.
type verifier struct {
	mu      sync.Mutex
	tokens  map[string]time.Time // token -> expiry (zero: none)
	outages int
}

func (v *verifier) VerifySession(context.Context, string) (*authn.Principal, error) {
	return nil, apierr.New(apierr.Unauthenticated, "no")
}

func (v *verifier) VerifyAccessToken(ctx context.Context, token string) (*authn.Principal, error) {
	return v.VerifyServiceToken(ctx, token)
}

func (v *verifier) VerifyServiceToken(_ context.Context, token string) (*authn.Principal, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.outages > 0 {
		v.outages--
		return nil, errors.New("database down")
	}
	exp, ok := v.tokens[token]
	if !ok || !exp.IsZero() && time.Now().After(exp) {
		return nil, apierr.New(apierr.Unauthenticated, "invalid credential")
	}
	return &authn.Principal{Kind: authn.KindServiceAccount, ServiceAccountID: uuid.New(), ProjectNumber: 482915736204, ExpiresAt: exp}, nil
}

func (v *verifier) revoke(token string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.tokens, token)
}

type owner struct{}

func (owner) GetProject(_ context.Context, p *authn.Principal, ref string) (tcore.Project, error) {
	if p.ProjectNumber != 482915736204 || ref != "chat-app" {
		return tcore.Project{}, apierr.New(apierr.NotFound, "Requested entity was not found.")
	}
	return tcore.Project{Number: 482915736204, ID: "chat-app"}, nil
}

const sat = authn.ServiceTokenPrefix + "valid"

func newTestAPI(t *testing.T, opts Options, co core.Options) (*API, *memStore, *verifier) {
	t.Helper()
	store := &memStore{roots: map[string]*core.Node{}, seq: map[string]int64{}}
	if co.Limits == (core.Limits{}) {
		co.Limits = core.DefaultLimits
	}
	svc := core.New(store, projects{"chat-app": 482915736204, "other-app": 582915736204}, co, discard)
	t.Cleanup(func() { svc.Close(context.Background()) })
	v := &verifier{tokens: map[string]time.Time{sat: {}}}
	if opts.Credentials == nil {
		opts.Credentials, opts.Projects = v, owner{}
	}
	a := New(svc, opts, discard)
	// Long-poll connections outlive their requests: close them before the service.
	t.Cleanup(func() { a.Shutdown(context.Background()) })
	return a, store, v
}

// client is a raw protocol connection to an API served by httptest.
type client struct {
	t   *testing.T
	ws  *websocket.Conn
	ctx context.Context
	rid int64
}

func serve(t *testing.T, a *API) string {
	t.Helper()
	ts := httptest.NewServer(a)
	t.Cleanup(ts.Close)
	return "ws" + strings.TrimPrefix(ts.URL, "http") + "/.ws?v=5&ns=chat-app-default-rtdb"
}

func dial(t *testing.T, url string) *client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	ws, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	ws.SetReadLimit(1 << 20)
	t.Cleanup(func() { ws.CloseNow() })
	c := &client{t: t, ws: ws, ctx: ctx}
	if h := c.read(); !strings.Contains(h, `"t":"h"`) {
		t.Fatalf("hello: %s", h)
	}
	return c
}

func (c *client) read() string {
	c.t.Helper()
	_, b, err := c.ws.Read(c.ctx)
	if err != nil {
		c.t.Fatalf("read: %v", err)
	}
	return string(b)
}

func (c *client) send(msg string) {
	c.t.Helper()
	for _, f := range wire.Split([]byte(msg)) {
		if err := c.ws.Write(c.ctx, websocket.MessageText, f); err != nil {
			c.t.Fatal(err)
		}
	}
}

// call sends a request and returns the next message, which must be its reply.
func (c *client) call(action string, body any) (status string, data string) {
	c.t.Helper()
	c.rid++
	b, _ := json.Marshal(map[string]any{"t": "d", "d": map[string]any{"r": c.rid, "a": action, "b": body}})
	c.send(string(b))
	var env struct {
		D struct {
			R int64 `json:"r"`
			B struct {
				S string          `json:"s"`
				D json.RawMessage `json:"d"`
			} `json:"b"`
		} `json:"d"`
	}
	m := c.read()
	if err := json.Unmarshal([]byte(m), &env); err != nil || env.D.R != c.rid {
		c.t.Fatalf("reply to %d: %s", c.rid, m)
	}
	return env.D.B.S, string(env.D.B.D)
}

func TestNamespace(t *testing.T) {
	a, _, _ := newTestAPI(t, Options{Domain: "rtdb.example.com", TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}}, core.Options{})
	cases := []struct {
		peer, host, forwarded, query, want string
		err                                bool
	}{
		{"", "chat-app-default-rtdb.rtdb.example.com", "", "", "chat-app-default-rtdb", false},
		{"", "chat-app-default-rtdb.rtdb.example.com:8443", "", "", "chat-app-default-rtdb", false},
		{"", "CHAT-APP-default-rtdb.RTDB.example.com", "", "", "chat-app-default-rtdb", false},
		// A fully qualified host keeps its trailing dot (review #16).
		{"", "chat-app-default-rtdb.rtdb.example.com.", "", "", "chat-app-default-rtdb", false},
		{"", "chat-app-default-rtdb.rtdb.example.com", "", "ns=chat-app-default-rtdb", "chat-app-default-rtdb", false},
		{"", "chat-app-default-rtdb.rtdb.example.com", "", "ns=other-app-default-rtdb", "", true}, // ADR-018: 400
		{"", "aruma.example.com", "", "ns=chat-app-default-rtdb", "chat-app-default-rtdb", false},
		{"", "127.0.0.1:8080", "", "ns=chat-app-default-rtdb", "chat-app-default-rtdb", false},
		{"", "a.b.rtdb.example.com", "", "ns=chat-app-default-rtdb", "chat-app-default-rtdb", false}, // not a database host
		{"", "aruma.example.com", "", "", "", true},
		// Behind a trusted proxy the forwarded host counts; from anyone else it does not
		// (review #11).
		{"10.0.0.5:1", "backend:8080", "chat-app-default-rtdb.rtdb.example.com", "", "chat-app-default-rtdb", false},
		{"192.0.2.1:1", "backend:8080", "chat-app-default-rtdb.rtdb.example.com", "", "", true},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/.ws?"+c.query, nil)
		r.Host = c.host
		if c.peer != "" {
			r.RemoteAddr = c.peer
		}
		if c.forwarded != "" {
			r.Header.Set("X-Forwarded-Host", c.forwarded)
		}
		got, err := a.namespace(r)
		if (err != nil) != c.err || got != c.want {
			t.Errorf("%s (fwd %q) ?%s: %q %v, want %q", c.host, c.forwarded, c.query, got, err, c.want)
		}
	}
	// Without rtdb.domain only ?ns= names a database.
	b, _, _ := newTestAPI(t, Options{}, core.Options{})
	r := httptest.NewRequest("GET", "/.ws", nil)
	r.Host = "chat-app-default-rtdb.rtdb.example.com"
	if _, err := b.namespace(r); err == nil {
		t.Error("a host names nothing without rtdb.domain")
	}
}

func TestRoutes(t *testing.T) {
	a, _, _ := newTestAPI(t, Options{}, core.Options{})
	for _, c := range []struct {
		path string
		code int
	}{
		{"/.ws?ns=missing-default-rtdb", http.StatusNotFound},
		// The control plane being down is no missing database (review #13).
		{"/.ws?ns=outage-default-rtdb", http.StatusServiceUnavailable},
		{"/.ws", http.StatusBadRequest},
		{"/.lp?ns=chat-app-default-rtdb&start=t", http.StatusOK},
		{"/x.json?ns=chat-app-default-rtdb", http.StatusNotImplemented},
		{"/x?ns=chat-app-default-rtdb", http.StatusBadRequest},
		{"/.ws?ns=chat-app-default-rtdb", http.StatusUpgradeRequired}, // not an upgrade
	} {
		rec := httptest.NewRecorder()
		a.ServeHTTP(rec, httptest.NewRequest("GET", c.path, nil))
		if rec.Code != c.code {
			t.Errorf("%s: %d %s, want %d", c.path, rec.Code, rec.Body, c.code)
		}
	}
}

func TestOriginAllowed(t *testing.T) {
	// Firebase checks no Origin, and by default neither does Aruma (lead decision).
	a, _, _ := newTestAPI(t, Options{}, core.Options{})
	r := httptest.NewRequest("GET", "/.ws", nil)
	r.Header.Set("Origin", "https://any.example.org")
	if !a.originAllowed(r, "db.example.com") {
		t.Error("an origin was refused without rtdb.allowed_origins")
	}
	// rtdb.allowed_origins narrows it.
	b, _, _ := newTestAPI(t, Options{AllowedOrigins: []string{"localhost", "app.example.com"}}, core.Options{})
	for origin, want := range map[string]bool{
		"":                                true,
		"https://db.example.com":          true, // the database's own host
		"http://localhost:5173":           true,
		"https://APP.example.com":         true,
		"https://evil.example.com":        false,
		"https://app.example.com.evil.io": false,
		"null":                            false,
	} {
		r := httptest.NewRequest("GET", "/.ws", nil)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if got := b.originAllowed(r, "db.example.com"); got != want {
			t.Errorf("origin %q: %v, want %v", origin, got, want)
		}
	}
}

func TestSessionID(t *testing.T) {
	re := regexp.MustCompile(`^[A-Za-z0-9]{32}$`)
	seen := map[string]bool{}
	for range 100 {
		id := sessionID()
		if !re.MatchString(id) || seen[id] {
			t.Fatalf("session id %q", id)
		}
		seen[id] = true
	}
}

func TestOutbox(t *testing.T) {
	o := newOutbox(10)
	if !o.push([]byte("12345")) || !o.push([]byte("67890")) {
		t.Fatal("within the limit")
	}
	msgs, closed, over := o.take()
	if len(msgs) != 2 || closed || over {
		t.Fatalf("take: %q %v %v", msgs, closed, over)
	}
	// Taking frees the space.
	o.push([]byte("1234567890"))
	if o.push([]byte("x")) {
		t.Fatal("over the limit was queued")
	}
	if msgs, closed, over := o.take(); len(msgs) != 0 || !closed || !over {
		t.Fatalf("overflow: %q %v %v", msgs, closed, over)
	}
	if o.push([]byte("y")) {
		t.Error("a closed outbox took a message")
	}
}

func TestReplyOrder(t *testing.T) {
	a, _, _ := newTestAPI(t, Options{}, core.Options{})
	s := &session{api: a, out: newOutbox(1 << 20)}
	first, second, third := s.reserve(1), s.reserve(2), s.reserve(3)
	s.fill(third, []byte("3"))
	s.fill(second, []byte("2"))
	if msgs, _, _ := s.out.take(); len(msgs) != 0 {
		t.Fatalf("replies overtook the first: %q", msgs)
	}
	s.fill(first, []byte("1"))
	if msgs, _, _ := s.out.take(); len(msgs) != 3 || string(msgs[0]) != "1" || string(msgs[2]) != "3" {
		t.Fatalf("order: %q", msgs)
	}
}

// A reply bigger than the whole outbox is too_big, and the connection stays (review #9).
func TestHugeReplyIsTooBig(t *testing.T) {
	lim := core.DefaultLimits
	lim.OutboxBytes = 64 << 10
	a, _, _ := newTestAPI(t, Options{}, core.Options{DevOpenRules: true, Limits: lim})
	c := dial(t, serve(t, a))
	big := strings.Repeat("x", 100<<10)
	if s, _ := c.call("p", map[string]any{"p": "/big", "d": big}); s != "ok" {
		t.Fatalf("put: %s", s)
	}
	if s, d := c.call("g", map[string]any{"p": "/big", "q": map[string]any{}}); s != wire.StatusTooBig {
		t.Errorf("huge get: %s %.60s", s, d)
	}
	if s, _ := c.call("g", map[string]any{"p": "/none", "q": map[string]any{}}); s != "ok" {
		t.Errorf("after a too_big reply: %s", s)
	}
}

// Shutdown closes connections with 1001 and no s, so clients reconnect (review #1).
func TestShutdownClosesWithoutS(t *testing.T) {
	a, store, _ := newTestAPI(t, Options{}, core.Options{DevOpenRules: true})
	url := serve(t, a)
	c := dial(t, url)
	if s, _ := c.call("o", map[string]any{"p": "/presence/me", "d": "offline"}); s != "ok" {
		t.Fatalf("onDisconnect: %s", s)
	}
	// The client keeps reading, as the SDKs do, so the close handshake completes.
	shut := make(chan error, 1)
	go func() { shut <- a.Shutdown(c.ctx) }()
	typ, b, err := c.ws.Read(c.ctx)
	var ce websocket.CloseError
	if !errors.As(err, &ce) || ce.Code != websocket.StatusGoingAway {
		t.Errorf("after shutdown the client got %v %q %v, want close 1001", typ, b, err)
	}
	if err := <-shut; err != nil {
		t.Fatal(err)
	}
	if got := store.value("chat-app-default-rtdb").Get(core.Path{"presence", "me"}).StringValue(); got != "offline" {
		t.Errorf("onDisconnect at shutdown: %q", got)
	}
	// No new connection once shutting down, and none slips in between (review #12).
	if _, resp, err := websocket.Dial(c.ctx, url, nil); err == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("dial during shutdown: %v", err)
	}
	if a.track(&wsConn{}) {
		t.Error("a connection was tracked after shutdown began")
	}
}

func TestConnectionsPerIP(t *testing.T) {
	lim := core.DefaultLimits
	lim.ConnectionsPerIP = 2
	a, _, _ := newTestAPI(t, Options{}, core.Options{Limits: lim})
	url := serve(t, a)
	first := dial(t, url)
	dial(t, url)
	if _, resp, err := websocket.Dial(first.ctx, url, nil); err == nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("third connection from one address: %v", err)
	}
	first.ws.Close(websocket.StatusNormalClosure, "")
	deadline := time.Now().Add(5 * time.Second)
	for {
		ws, _, err := websocket.Dial(context.Background(), url, nil)
		if err == nil {
			ws.CloseNow()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("slot not freed: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Before signing in, a connection may only send small messages; partly received messages
// share a global budget (review #3).
func TestMessageLimits(t *testing.T) {
	lim := core.DefaultLimits
	lim.UnauthenticatedMessageBytes = 4 << 10
	lim.InflightBytes = 40 << 10
	a, _, _ := newTestAPI(t, Options{}, core.Options{Limits: lim})
	c := dial(t, serve(t, a))
	errorFrame := string(wire.EncodeError(errorFrame))
	medium := `{"t":"d","d":{"r":1,"a":"p","b":{"p":"/m","d":"` + strings.Repeat("x", 8<<10) + `"}}}`
	c.send(medium)
	if m := c.read(); m != errorFrame {
		t.Errorf("8 KB before signing in: %s", m)
	}
	if s, _ := c.call("gauth", map[string]any{"cred": sat}); s != "ok" {
		t.Fatalf("gauth: %s", s)
	}
	c.rid = 1
	c.send(medium)
	if m := c.read(); !strings.Contains(m, `"s":"ok"`) {
		t.Errorf("8 KB after signing in: %s", m)
	}
	// 50 KB in frames is more than the budget for messages in transit.
	c.send(`{"t":"d","d":{"r":2,"a":"p","b":{"p":"/m","d":"` + strings.Repeat("x", 50<<10) + `"}}}`)
	if m := c.read(); m != errorFrame {
		t.Errorf("over the in-flight budget: %s", m)
	}
	a.inflight.mu.Lock()
	left := a.inflight.left
	a.inflight.mu.Unlock()
	if left != lim.InflightBytes {
		t.Errorf("budget not returned: %d of %d", left, lim.InflightBytes)
	}
}

func TestAuth(t *testing.T) {
	a, _, _ := newTestAPI(t, Options{}, core.Options{})
	c := dial(t, serve(t, a))
	denied := func(what string) {
		t.Helper()
		if s, _ := c.call("p", map[string]any{"p": "/a", "d": 1}); s != "permission_denied" {
			t.Errorf("%s: write %s", what, s)
		}
	}
	denied("no credential")
	// An auth variable override is no admin (review #4).
	if s, _ := c.call("gauth", map[string]any{"cred": sat, "authvar": map[string]any{"uid": "u1"}}); s != "ok" {
		t.Fatalf("gauth with authvar: %s", s)
	}
	denied("authvar")
	if s, _ := c.call("gauth", map[string]any{"cred": sat, "noauth": true}); s != "ok" {
		t.Fatalf("gauth with noauth: %s", s)
	}
	denied("noauth")
	// The admin credential itself, announcing its real expiry (none here).
	if s, d := c.call("gauth", map[string]any{"cred": sat}); s != "ok" || d != `{"auth":null,"expires":null}` {
		t.Fatalf("gauth: %s %s", s, d)
	}
	if s, _ := c.call("p", map[string]any{"p": "/a", "d": 1}); s != "ok" {
		t.Errorf("admin write: %s", s)
	}
	if s, _ := c.call("gauth", map[string]any{"cred": authn.ServiceTokenPrefix + "unknown"}); s != wire.StatusInvalidToken {
		t.Errorf("bad token: %s", s)
	}
	denied("after a refused token")
}

// An admin credential is dropped when it expires or stops verifying, with ac (review #5).
func TestAdminCredentialRevalidated(t *testing.T) {
	old := revalidateEvery
	revalidateEvery = 30 * time.Millisecond
	defer func() { revalidateEvery = old }()
	a, _, v := newTestAPI(t, Options{}, core.Options{})
	url := serve(t, a)

	expiring := authn.ServiceTokenPrefix + "expiring"
	exp := time.Now().Add(150 * time.Millisecond)
	v.mu.Lock()
	v.tokens[expiring] = exp
	v.mu.Unlock()
	c := dial(t, url)
	if s, d := c.call("gauth", map[string]any{"cred": expiring}); s != "ok" || !strings.Contains(d, `"expires":`+itoa(exp.Unix())) {
		t.Fatalf("gauth: %s %s", s, d)
	}
	if m := c.read(); m != string(wire.EncodeAuthRevoked(wire.StatusExpiredToken, "Auth token is expired.")) {
		t.Errorf("at expiry: %s", m)
	}
	if s, _ := c.call("p", map[string]any{"p": "/a", "d": 1}); s != "permission_denied" {
		t.Errorf("after expiry: %s", s)
	}

	// A check that cannot reach the database changes nothing; a revoked token is dropped.
	d := dial(t, url)
	if s, _ := d.call("gauth", map[string]any{"cred": sat}); s != "ok" {
		t.Fatal(s)
	}
	v.mu.Lock()
	v.outages = 1
	v.mu.Unlock()
	time.Sleep(50 * time.Millisecond)
	if s, _ := d.call("p", map[string]any{"p": "/a", "d": 1}); s != "ok" {
		t.Errorf("after a failed check: %s", s)
	}
	v.revoke(sat)
	if m := d.read(); m != string(wire.EncodeAuthRevoked(wire.StatusInvalidToken, "Auth token is no longer valid.")) {
		t.Errorf("after revocation: %s", m)
	}
	if s, _ := d.call("p", map[string]any{"p": "/a", "d": 1}); s != "permission_denied" {
		t.Errorf("after revocation: %s", s)
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }
