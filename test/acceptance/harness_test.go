// Package acceptance runs the Phase 1 acceptance cases (AC-*) and isolation cases against a
// fully wired server over real HTTP, each test on its own fresh database.
package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arumadev/aruma/internal/app"
	"github.com/arumadev/aruma/internal/config"
	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/platform/crypto"
	"github.com/arumadev/aruma/internal/platform/log"
	"github.com/arumadev/aruma/internal/store"
	"github.com/arumadev/aruma/internal/store/storetest"
	"github.com/arumadev/aruma/internal/tenancy/core"
)

const (
	testMasterKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="
	testPassword  = "correct horse battery"
)

// fastArgon2 keeps password hashing cheap in tests.
var fastArgon2 = crypto.Argon2Params{Memory: 64, Time: 1, Threads: 1}

// Env is one running server on a fresh database.
type Env struct {
	t    *testing.T
	URL  string
	App  *app.App
	Pool *pgxpool.Pool
	DB   string // database URL
	Logs *syncBuffer
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// newEnv starts a server. env overrides ARUMA_* settings; opts adjust the wiring.
func newEnv(t *testing.T, env map[string]string, opts ...app.Option) *Env {
	t.Helper()
	return newEnvWithPool(t, 0, env, opts...)
}

// newEnvWithPool is newEnv with the server's database pool capped at maxConns connections, as
// pool_max_conns in ARUMA_DATABASE_URL caps it; 0 keeps pgx's default.
func newEnvWithPool(t *testing.T, maxConns int, env map[string]string, opts ...app.Option) *Env {
	t.Helper()
	dbURL := storetest.NewDB(t)
	ctx := context.Background()
	poolURL := dbURL
	if maxConns > 0 {
		u, err := url.Parse(dbURL)
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		q.Set("pool_max_conns", strconv.Itoa(maxConns))
		u.RawQuery = q.Encode()
		poolURL = u.String()
	}
	pool, err := store.Open(ctx, poolURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + ln.Addr().String()
	vars := map[string]string{
		"ARUMA_DATABASE_URL": dbURL,
		"ARUMA_PUBLIC_URL":   base,
		"ARUMA_MASTER_KEY":   testMasterKey,
	}
	for k, v := range env {
		vars[k] = v
	}
	cfg, err := config.Load("", func(k string) string { return vars[k] })
	if err != nil {
		t.Fatal(err)
	}
	logs := &syncBuffer{}
	a, err := app.New(cfg, log.New(logs, "debug"), "test", pool, append([]app.Option{app.WithArgon2(fastArgon2)}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewUnstartedServer(a.Server.Handler())
	ts.Listener.Close()
	ts.Listener = ln
	ts.Start()
	t.Cleanup(ts.Close)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("server log:\n%s", logs.String())
		}
	})
	return &Env{t: t, URL: base, App: a, Pool: pool, DB: dbURL, Logs: logs}
}

var setupCodeRe = regexp.MustCompile(`"code":"([A-Za-z0-9_-]+)"`)

// setupCode announces setup and reads the code back from the log, as an operator would.
func (e *Env) setupCode() string {
	e.t.Helper()
	if err := e.App.AnnounceSetup(context.Background()); err != nil {
		e.t.Fatal(err)
	}
	m := setupCodeRe.FindAllStringSubmatch(e.Logs.String(), -1)
	if len(m) == 0 {
		e.t.Fatal("setup code not logged")
	}
	return m[len(m)-1][1]
}

// owner completes setup and returns the signed-in first account.
func (e *Env) owner() *Client {
	e.t.Helper()
	c := e.client()
	c.mustOK(c.do("POST", "/console/v1/setup", map[string]any{
		"setupCode": e.setupCode(), "email": "owner@example.com", "password": testPassword, "displayName": "Owner",
	}))
	return c
}

// account creates another account the way the operator command does, then signs it in.
func (e *Env) account(email string) *Client {
	e.t.Helper()
	if _, err := e.App.Service.CreateAccount(context.Background(), core.NewAccount{Email: email, Password: testPassword}); err != nil {
		e.t.Fatal(err)
	}
	c := e.client()
	c.login(email, testPassword)
	return c
}

// Client is a browser-like API client: it keeps cookies and sends the CSRF header.
type Client struct {
	env     *Env
	cookies map[string]*http.Cookie
	csrf    string
	bearer  string
	header  map[string]string
}

func (e *Env) client() *Client {
	return &Client{env: e, cookies: map[string]*http.Cookie{}, header: map[string]string{}}
}

// withBearer returns a client that authenticates with a bearer token only.
func (e *Env) withBearer(token string) *Client {
	c := e.client()
	c.bearer = token
	return c
}

// Response is a decoded API response.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
	JSON   map[string]any
}

// str walks nested objects and returns the string at the end of path.
func (r *Response) str(path ...string) string {
	var v any = r.JSON
	for _, p := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return ""
		}
		v = m[p]
	}
	s, _ := v.(string)
	return s
}

// list returns the array at key.
func (r *Response) list(key string) []map[string]any {
	raw, _ := r.JSON[key].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, v := range raw {
		if m, ok := v.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func (r *Response) errStatus() string { return r.str("error", "status") }

func (c *Client) do(method, path string, body any) *Response {
	c.env.t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			c.env.t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.env.URL+path, rd)
	if err != nil {
		c.env.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, ck := range c.cookies {
		req.AddCookie(&http.Cookie{Name: ck.Name, Value: ck.Value})
	}
	if c.csrf != "" {
		req.Header.Set(authn.CSRFHeader, c.csrf)
	}
	if c.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearer)
	}
	for k, v := range c.header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.env.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	for _, ck := range resp.Cookies() {
		if ck.MaxAge < 0 || ck.Value == "" {
			delete(c.cookies, ck.Name)
		} else {
			c.cookies[ck.Name] = ck
		}
	}
	out := &Response{Status: resp.StatusCode, Header: resp.Header, Body: b}
	_ = json.Unmarshal(b, &out.JSON)
	if tok := out.str("csrfToken"); tok != "" {
		c.csrf = tok
	}
	return out
}

func (c *Client) mustOK(r *Response) *Response {
	c.env.t.Helper()
	if r.Status < 200 || r.Status > 299 {
		c.env.t.Fatalf("status %d: %s", r.Status, r.Body)
	}
	return r
}

func (c *Client) login(email, password string) *Response {
	c.env.t.Helper()
	return c.mustOK(c.do("POST", "/console/v1/auth:login", map[string]any{"email": email, "password": password}))
}

func expect(t *testing.T, r *Response, status int, errStatus string) {
	t.Helper()
	if r.Status != status || (errStatus != "" && r.errStatus() != errStatus) {
		t.Fatalf("got %d %s, want %d %s: %s", r.Status, r.errStatus(), status, errStatus, strings.TrimSpace(string(r.Body)))
	}
}
