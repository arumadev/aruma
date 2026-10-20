package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/osama300/aurma-project/internal/services/rtdb/core"
	"github.com/osama300/aurma-project/internal/services/rtdb/wire"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

// memStore keeps databases in memory, on core.Node.Set.
type memStore struct {
	mu    sync.Mutex
	roots map[string]*core.Node
}

func (s *memStore) OpenDatabase(_ context.Context, project int64, name string) (core.DatabaseInfo, error) {
	return core.DatabaseInfo{ID: 1, Project: project, Name: name}, nil
}

func (s *memStore) Begin(_ context.Context, db core.DatabaseInfo) (core.StoreTx, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return &memTx{s: s, name: db.Name, root: s.roots[db.Name]}, nil
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
}

func (t *memTx) Read(_ context.Context, p core.Path) (*core.Node, error) { return t.root.Get(p), nil }
func (t *memTx) Write(_ context.Context, p core.Path, v *core.Node) error {
	t.root = t.root.Set(p, v)
	return nil
}
func (t *memTx) AdvanceSeq(context.Context, int64) (int64, error) { return 0, nil }
func (t *memTx) Rollback(context.Context) error                   { return nil }
func (t *memTx) Commit(context.Context) error {
	t.s.mu.Lock()
	defer t.s.mu.Unlock()
	t.s.roots[t.name] = t.root
	return nil
}

type projects map[string]int64

func (p projects) ProjectNumber(_ context.Context, id string) (int64, error) {
	if n, ok := p[id]; ok {
		return n, nil
	}
	return 0, errors.New("not found")
}

func newTestAPI(t *testing.T, opts Options) (*API, *memStore) {
	store := &memStore{roots: map[string]*core.Node{}}
	svc := core.New(store, projects{"chat-app": 482915736204, "other-app": 582915736204}, core.Options{DevOpenRules: true}, discard)
	t.Cleanup(func() { svc.Close(context.Background()) })
	return New(svc, opts, discard), store
}

func TestNamespace(t *testing.T) {
	a, _ := newTestAPI(t, Options{Domain: "rtdb.example.com"})
	cases := []struct {
		host, query, want string
		err               bool
	}{
		{"chat-app-default-rtdb.rtdb.example.com", "", "chat-app-default-rtdb", false},
		{"chat-app-default-rtdb.rtdb.example.com:8443", "", "chat-app-default-rtdb", false},
		{"CHAT-APP-default-rtdb.RTDB.example.com", "", "chat-app-default-rtdb", false},
		{"chat-app-default-rtdb.rtdb.example.com", "ns=chat-app-default-rtdb", "chat-app-default-rtdb", false},
		{"chat-app-default-rtdb.rtdb.example.com", "ns=other-app-default-rtdb", "", true}, // ADR-018: 400
		{"aruma.example.com", "ns=chat-app-default-rtdb", "chat-app-default-rtdb", false},
		{"127.0.0.1:8080", "ns=chat-app-default-rtdb", "chat-app-default-rtdb", false},
		{"a.b.rtdb.example.com", "ns=chat-app-default-rtdb", "chat-app-default-rtdb", false}, // not a database host
		{"aruma.example.com", "", "", true},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/.ws?"+c.query, nil)
		r.Host = c.host
		got, err := a.namespace(r)
		if (err != nil) != c.err || got != c.want {
			t.Errorf("%s ?%s: %q %v, want %q", c.host, c.query, got, err, c.want)
		}
	}
	// Without rtdb.domain only ?ns= names a database.
	b, _ := newTestAPI(t, Options{})
	r := httptest.NewRequest("GET", "/.ws", nil)
	r.Host = "chat-app-default-rtdb.rtdb.example.com"
	if _, err := b.namespace(r); err == nil {
		t.Error("a host names nothing without rtdb.domain")
	}
}

func TestRoutes(t *testing.T) {
	a, _ := newTestAPI(t, Options{})
	for _, c := range []struct {
		path string
		code int
	}{
		{"/.ws?ns=missing-default-rtdb", http.StatusNotFound},
		{"/.ws", http.StatusBadRequest},
		{"/.lp?ns=chat-app-default-rtdb&start=t", http.StatusNotFound},
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
	a, _ := newTestAPI(t, Options{AllowedOrigins: func(_ context.Context, project int64) ([]string, error) {
		if project != 482915736204 {
			t.Errorf("asked for project %d", project)
		}
		return []string{"localhost", "app.example.com"}, nil
	}})
	target := core.Target{Name: "chat-app-default-rtdb", ProjectID: "chat-app", Project: 482915736204}
	for origin, want := range map[string]bool{
		"":                                true,
		"https://db.example.com":          true, // the request's own host
		"http://localhost:5173":           true,
		"https://APP.example.com":         true,
		"https://evil.example.com":        false,
		"https://app.example.com.evil.io": false,
		"null":                            false,
		"://":                             false,
	} {
		r := httptest.NewRequest("GET", "/.ws", nil)
		r.Host = "db.example.com"
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if got := a.originAllowed(r, target); got != want {
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

func TestShutdownTellsClientsAndRunsOnDisconnect(t *testing.T) {
	a, store := newTestAPI(t, Options{})
	ts := httptest.NewServer(a)
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/.ws?v=5&ns=chat-app-default-rtdb", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	read := func() string {
		_, b, err := ws.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		return string(b)
	}
	if h := read(); !strings.Contains(h, `"t":"h"`) {
		t.Fatalf("hello: %s", h)
	}
	ws.Write(ctx, websocket.MessageText, []byte(`{"t":"d","d":{"r":1,"a":"o","b":{"p":"/presence/me","d":"offline"}}}`))
	if r := read(); r != `{"t":"d","d":{"r":1,"b":{"s":"ok","d":""}}}` {
		t.Fatalf("onDisconnect: %s", r)
	}
	// The client keeps reading, as the SDKs do, so the close handshake completes.
	shut := make(chan error, 1)
	go func() { shut <- a.Shutdown(ctx) }()
	if m := read(); m != string(wire.EncodeShutdown(shutdownReason)) {
		t.Errorf("shutdown frame: %s", m)
	}
	var ce websocket.CloseError
	if _, _, err := ws.Read(ctx); !errors.As(err, &ce) || ce.Code != websocket.StatusGoingAway {
		t.Errorf("close: %v", err)
	}
	if err := <-shut; err != nil {
		t.Fatal(err)
	}
	if got := store.value("chat-app-default-rtdb").Get(core.Path{"presence", "me"}).StringValue(); got != "offline" {
		t.Errorf("onDisconnect at shutdown: %q", got)
	}
	// No new connection once shutting down.
	if _, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/.ws?v=5&ns=chat-app-default-rtdb", nil); err == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("dial during shutdown: %v", err)
	}
}

func TestReplyOrder(t *testing.T) {
	a, _ := newTestAPI(t, Options{})
	s := &session{api: a, out: newOutbox(1 << 20)}
	first, second, third := s.reserve(), s.reserve(), s.reserve()
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
