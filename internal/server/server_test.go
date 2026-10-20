package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/osama300/aurma-project/internal/config"
	"github.com/osama300/aurma-project/internal/platform/metrics"
	"github.com/osama300/aurma-project/internal/store"
	"github.com/osama300/aurma-project/internal/store/storetest"
)

func testConfig(t *testing.T, publicURL string) *config.Config {
	t.Helper()
	cfg, err := config.Load("", func(k string) string {
		return map[string]string{
			"ARUMA_DATABASE_URL": "postgres://unused",
			"ARUMA_PUBLIC_URL":   publicURL,
			"ARUMA_MASTER_KEY":   "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
		}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func newServer(t *testing.T, pool *pgxpool.Pool, h Handlers) *httptest.Server {
	t.Helper()
	s := New(testConfig(t, "https://aruma.test"), slog.New(slog.NewTextHandler(io.Discard, nil)), pool, metrics.New("test"), h)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func get(t *testing.T, ts *httptest.Server, method, path string, header map[string]string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, ts.URL+path, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// S-8, S-9: security headers everywhere, and never a CORS header.
func TestSecurityHeadersAndNoCORS(t *testing.T) {
	ts := newServer(t, nil, Handlers{})
	for _, path := range []string{"/healthz", "/v1beta1/projects", "/console/v1/me", "/anything"} {
		resp := get(t, ts, "GET", path, map[string]string{"Origin": "https://evil.example"})
		h := resp.Header
		if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("X-Frame-Options") != "DENY" ||
			!strings.Contains(h.Get("Content-Security-Policy"), "frame-ancestors 'none'") ||
			h.Get("Strict-Transport-Security") == "" {
			t.Errorf("%s: missing security headers: %v", path, h)
		}
		if h.Get("Access-Control-Allow-Origin") != "" || h.Get("Access-Control-Allow-Credentials") != "" {
			t.Errorf("%s: CORS header present", path)
		}
	}
	pre := get(t, ts, "OPTIONS", "/console/v1/me", map[string]string{
		"Origin": "https://evil.example", "Access-Control-Request-Method": "POST",
	})
	if pre.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Error("preflight answered with CORS")
	}
}

// S-12: metrics are not served on the public entry point.
func TestMetricsNotPublic(t *testing.T) {
	ts := newServer(t, nil, Handlers{})
	if resp := get(t, ts, "GET", "/metrics", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("/metrics on public listener: %d", resp.StatusCode)
	}
}

func TestUnmountedAPIsAnswerGoogleNotFound(t *testing.T) {
	ts := newServer(t, nil, Handlers{})
	resp := get(t, ts, "GET", "/v1beta1/projects", nil)
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 404 || !strings.Contains(string(body), `"status":"NOT_FOUND"`) {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
}

func TestReadiness(t *testing.T) {
	ctx := context.Background()
	empty, err := store.Open(ctx, storetest.NewEmptyDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer empty.Close()
	if resp := get(t, newServer(t, empty, Handlers{}), "GET", "/readyz", nil); resp.StatusCode != 503 {
		t.Fatalf("readyz on unmigrated database: %d", resp.StatusCode)
	}
	if resp := get(t, newServer(t, storetest.NewPool(t), Handlers{}), "GET", "/readyz", nil); resp.StatusCode != 200 {
		t.Fatalf("readyz on migrated database: %d", resp.StatusCode)
	}
	// During a deploy the new version migrates first; the old instances stay ready.
	ahead := storetest.NewPool(t)
	if _, err := ahead.Exec(ctx, "INSERT INTO public.goose_auth (version_id, is_applied) SELECT max(version_id) + 1, true FROM public.goose_auth"); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckCurrent(ctx, ahead); !errors.Is(err, store.ErrSchemaNewer) {
		t.Fatalf("CheckCurrent on a newer schema = %v, want ErrSchemaNewer", err)
	}
	if resp := get(t, newServer(t, ahead, Handlers{}), "GET", "/readyz", nil); resp.StatusCode != 200 {
		t.Fatalf("readyz on a schema newer than the binary: %d", resp.StatusCode)
	}
	if resp := get(t, newServer(t, nil, Handlers{}), "GET", "/healthz", nil); resp.StatusCode != 200 {
		t.Fatalf("healthz: %d", resp.StatusCode)
	}
}

// ADR-018: the Realtime Database owns its hosts and a few paths of the main host.
func TestRTDBRouting(t *testing.T) {
	cfg, err := config.Load("", func(k string) string {
		return map[string]string{
			"ARUMA_DATABASE_URL":    "postgres://unused",
			"ARUMA_PUBLIC_URL":      "https://aruma.test",
			"ARUMA_MASTER_KEY":      "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
			"ARUMA_RTDB_DOMAIN":     "rtdb.aruma.test",
			"ARUMA_TRUSTED_PROXIES": "10.0.0.0/8",
		}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	var hits []string
	rtdb := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.Host+r.URL.RequestURI())
		// Bodies may exceed the 1 MB cap of the other APIs.
		n, err := io.Copy(io.Discard, r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
			return
		}
		w.Write([]byte(strconv.FormatInt(n, 10)))
	})
	s := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, metrics.New("test"), Handlers{RTDB: rtdb})
	h := s.Handler()
	cases := []struct {
		host, path string
		rtdb       bool
	}{
		{"chat-default-rtdb.rtdb.aruma.test", "/.ws?v=5", true},
		{"chat-default-rtdb.rtdb.aruma.test:443", "/users.json", true},
		{"Chat-Default-RTDB.RTDB.aruma.test", "/anything", true},
		{"aruma.test", "/.ws?v=5&ns=chat-default-rtdb", true},
		{"aruma.test", "/.lp?start=t", true},
		{"aruma.test", "/users.json?ns=chat-default-rtdb", true},
		{"aruma.test", "/users.json", false},
		{"aruma.test", "/healthz", false},
		{"rtdb.aruma.test", "/healthz", false},
		{"evil-rtdb.aruma.test", "/x.json", false},
	}
	for _, c := range cases {
		hits = nil
		r := httptest.NewRequest("GET", c.path, nil)
		r.Host = c.host
		h.ServeHTTP(httptest.NewRecorder(), r)
		if got := len(hits) == 1; got != c.rtdb {
			t.Errorf("%s%s: routed to RTDB = %v, want %v", c.host, c.path, got, c.rtdb)
		}
	}
	// A fully qualified host with its trailing dot (review #16), and the host a trusted
	// proxy forwards (review #11); an untrusted peer's X-Forwarded-Host is ignored.
	for _, c := range []struct {
		peer, host, forwarded string
		rtdb                  bool
	}{
		{"192.0.2.1:1", "chat-default-rtdb.rtdb.aruma.test.", "", true},
		{"192.0.2.1:1", "chat-default-rtdb.rtdb.aruma.test.:443", "", true},
		{"10.1.2.3:1", "aruma.test", "chat-default-rtdb.rtdb.aruma.test", true},
		{"192.0.2.1:1", "aruma.test", "chat-default-rtdb.rtdb.aruma.test", false},
		{"10.1.2.3:1", "chat-default-rtdb.rtdb.aruma.test", "aruma.test", false},
	} {
		hits = nil
		r := httptest.NewRequest("GET", "/x", nil)
		r.RemoteAddr, r.Host = c.peer, c.host
		if c.forwarded != "" {
			r.Header.Set("X-Forwarded-Host", c.forwarded)
		}
		h.ServeHTTP(httptest.NewRecorder(), r)
		if got := len(hits) == 1; got != c.rtdb {
			t.Errorf("peer %s host %s forwarded %q: routed to RTDB = %v, want %v", c.peer, c.host, c.forwarded, got, c.rtdb)
		}
	}

	big := strings.Repeat("x", 2<<20)
	r := httptest.NewRequest("PUT", "/x.json?ns=chat-default-rtdb", strings.NewReader(big))
	r.Host = "aruma.test"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK || rec.Body.String() != strconv.Itoa(len(big)) {
		t.Errorf("2 MB body: %d %s", rec.Code, rec.Body)
	}

	// Disabled, the paths fall through to the rest of the entry point.
	off := New(testConfig(t, "https://aruma.test"), slog.New(slog.NewTextHandler(io.Discard, nil)), nil, metrics.New("test"), Handlers{})
	rec = httptest.NewRecorder()
	off.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/.ws?ns=chat-default-rtdb", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("RTDB disabled: %d", rec.Code)
	}
}

type shutdownHandler struct {
	http.Handler
	called chan struct{}
}

func (h shutdownHandler) Shutdown(context.Context) error { close(h.called); return nil }

func TestRTDBShutdown(t *testing.T) {
	cfg := testConfig(t, "http://127.0.0.1:8080")
	cfg.Listen = "127.0.0.1:8621"
	cfg.MetricsListen = ""
	h := shutdownHandler{Handler: http.NotFoundHandler(), called: make(chan struct{})}
	s := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, metrics.New("test"), Handlers{RTDB: h})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case <-h.called:
	default:
		t.Error("Run did not shut the RTDB handler down")
	}
}

// Review H-1: client-chosen methods must not create metric series.
func TestMetricsMethodLabelIsBounded(t *testing.T) {
	m := metrics.New("test")
	s := New(testConfig(t, "https://aruma.test"), slog.New(slog.NewTextHandler(io.Discard, nil)), nil, m, Handlers{})
	h := s.Handler()
	for i := 0; i < 100; i++ {
		r := httptest.NewRequest("GET", "/v1beta1/x", nil)
		r.Method = strings.Repeat("X", 1000) + string(rune('A'+i%26)) + strconv.Itoa(i)
		h.ServeHTTP(httptest.NewRecorder(), r)
	}
	families, err := m.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() == "aruma_http_requests_total" && len(f.GetMetric()) > 8 {
			t.Fatalf("%d request series from random methods", len(f.GetMetric()))
		}
	}
}
