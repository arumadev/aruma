package httpx

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/arumadev/aruma/internal/platform/apierr"
)

func TestClientIP(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	cases := []struct {
		remote, xff, want string
	}{
		{"203.0.113.9:4000", "", "203.0.113.9"},
		{"203.0.113.9:4000", "1.2.3.4", "203.0.113.9"},              // untrusted peer: header ignored
		{"10.0.0.2:4000", "198.51.100.7", "198.51.100.7"},           // one trusted proxy
		{"10.0.0.2:4000", "6.6.6.6, 198.51.100.7", "198.51.100.7"},  // left part is forgeable
		{"10.0.0.2:4000", "198.51.100.7, 10.0.0.3", "198.51.100.7"}, // chain of proxies
		{"10.0.0.2:4000", "garbage", "10.0.0.2"},
		{"[::ffff:10.0.0.2]:4000", "198.51.100.7", "198.51.100.7"},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if got := ClientIP(r, trusted).String(); got != c.want {
			t.Errorf("remote %s xff %q: got %s, want %s", c.remote, c.xff, got, c.want)
		}
	}
}

func TestHost(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	cases := []struct {
		peer, host, forwarded, want string
	}{
		{"192.0.2.1:1", "db.example.com", "", "db.example.com"},
		{"192.0.2.1:1", "db.example.com:8443", "", "db.example.com:8443"},
		{"192.0.2.1:1", "db.example.com.", "", "db.example.com"},
		{"192.0.2.1:1", "db.example.com.:443", "", "db.example.com:443"},
		// Only a trusted proxy's X-Forwarded-Host counts; the first of a chain is the client's.
		{"10.0.0.7:1", "backend:8080", "db.example.com", "db.example.com"},
		{"10.0.0.7:1", "backend:8080", "db.example.com:443, proxy2", "db.example.com:443"},
		{"10.0.0.7:1", "backend:8080", "", "backend:8080"},
		{"192.0.2.1:1", "db.example.com", "evil.example.com", "db.example.com"},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr, r.Host = c.peer, c.host
		if c.forwarded != "" {
			r.Header.Set("X-Forwarded-Host", c.forwarded)
		}
		if got := Host(r, trusted); got != c.want {
			t.Errorf("peer %s host %s fwd %q: %q, want %q", c.peer, c.host, c.forwarded, got, c.want)
		}
	}
}

func TestPeerIsLoopback(t *testing.T) {
	for peer, want := range map[string]bool{"127.0.0.1:5": true, "[::1]:5": true, "192.0.2.1:5": false, "[::ffff:127.0.0.1]:5": true} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = peer
		r.Header.Set("X-Forwarded-For", "127.0.0.1") // never believed here
		if got := PeerIsLoopback(r); got != want {
			t.Errorf("%s: %v", peer, got)
		}
	}
}

func TestDecodeJSON(t *testing.T) {
	var v struct{ Name string }
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"x","extra":1}`))
	r.Header.Set("Content-Type", "application/json")
	if err := DecodeJSON(r, &v, false); err != nil || v.Name != "x" {
		t.Fatalf("lenient decode: %v %+v", err, v)
	}
	r = httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"x","extra":1}`))
	if err := DecodeJSON(r, &v, true); apierr.CodeOf(err) != apierr.InvalidArgument {
		t.Fatalf("strict decode accepted unknown field: %v", err)
	}
	r = httptest.NewRequest("POST", "/", strings.NewReader(`{"name":`))
	if err := DecodeJSON(r, &v, false); apierr.CodeOf(err) != apierr.InvalidArgument {
		t.Fatalf("broken JSON: %v", err)
	}
	r = httptest.NewRequest("POST", "/", strings.NewReader(`name=x`))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := DecodeJSON(r, &v, false); apierr.CodeOf(err) != apierr.InvalidArgument {
		t.Fatalf("form body accepted as JSON: %v", err)
	}
}

func TestCommonRecoversAndLimitsBody(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	h := Common(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/panic" {
			panic("boom")
		}
		var v map[string]any
		if err := DecodeJSON(r, &v, false); err != nil {
			apierr.Write(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}), logger, nil)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/panic?token=secret", nil))
	if rec.Code != 500 || !strings.Contains(rec.Body.String(), `"status":"INTERNAL"`) {
		t.Fatalf("panic: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(logs.String(), "token=secret") {
		t.Fatal("query string was logged")
	}
	if rec.Header().Get("X-Request-Id") == "" {
		t.Fatal("no request id")
	}

	rec = httptest.NewRecorder()
	big := `{"a":"` + strings.Repeat("x", MaxBodyBytes) + `"}`
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/", strings.NewReader(big)))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body: %d", rec.Code)
	}
}
