package server

import (
	"context"
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
	if resp := get(t, newServer(t, nil, Handlers{}), "GET", "/healthz", nil); resp.StatusCode != 200 {
		t.Fatalf("healthz: %d", resp.StatusCode)
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
