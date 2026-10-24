package console

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// concrete is a route's pattern on chat-app, with an object named where the route takes one.
func concrete(r route) string {
	return strings.Replace(r.pattern, "{project}", "chat-app", 1) + "?name=a.txt"
}

func safe(method string) bool { return method == "GET" || method == "HEAD" }

// Every route needs an account's credentials (ADR-010 S-4), and a session needs its CSRF token
// on every write (S-5); a CLI token needs none. Another account's project is not found.
func TestEveryRouteNeedsCredentialsAndCSRF(t *testing.T) {
	e := newEnv(t, true)
	e.insert("a.txt", "text/plain", "hello")
	if len(e.routes) < 15 {
		t.Fatalf("only %d routes registered", len(e.routes))
	}
	for _, r := range e.routes {
		target := concrete(r)
		if got := e.do(nobody, r.method, target, nil); got.code != http.StatusUnauthorized {
			t.Errorf("%s %s without credentials: %d %s, want 401", r.method, r.pattern, got.code, got.body)
		}
		if got := e.do(as{session: "session-forged", csrf: true}, r.method, target, nil); got.code != http.StatusUnauthorized {
			t.Errorf("%s %s with an unknown session: %d, want 401", r.method, r.pattern, got.code)
		}
		noCSRF := e.do(as{session: "session-amal"}, r.method, target, nil)
		switch {
		case !safe(r.method) && (noCSRF.code != http.StatusForbidden || !strings.Contains(string(noCSRF.body), "CSRF")):
			t.Errorf("%s %s with a session and no CSRF token: %d %s, want 403", r.method, r.pattern, noCSRF.code, noCSRF.body)
		case safe(r.method) && (noCSRF.code == http.StatusUnauthorized || noCSRF.code == http.StatusForbidden):
			t.Errorf("%s %s (a read) with a session: %d %s", r.method, r.pattern, noCSRF.code, noCSRF.body)
		}
		for _, who := range []as{amalSession, amalCLI} {
			if got := e.do(who, r.method, target, nil); got.code == http.StatusUnauthorized || got.code == http.StatusForbidden {
				t.Errorf("%s %s as the owner: %d %s", r.method, r.pattern, got.code, got.body)
			}
		}
		if got := e.do(badrSession, r.method, target, nil); got.code != http.StatusNotFound {
			t.Errorf("%s %s on a project of another account: %d %s, want 404", r.method, r.pattern, got.code, got.body)
		}
	}
}

// A write sent with another session's CSRF token is refused like one without.
func TestCSRFIsTheSessions(t *testing.T) {
	e := newEnv(t, true)
	req := httptest.NewRequest("POST", path("/uploads"), strings.NewReader(`{"name":"x","size":1}`))
	req.Header.Set("Content-Type", "application/json")
	e.authorize(req, as{session: "session-amal"})
	req.Header.Set("X-Aruma-CSRF", e.keys.CSRFToken("session-badr"))
	if got := e.serve(req); got.code != http.StatusForbidden {
		t.Errorf("another session's CSRF token: %d %s, want 403", got.code, got.body)
	}
	// And a browser's cross-origin write is refused before routing.
	req = httptest.NewRequest("POST", path("/uploads"), strings.NewReader(`{"name":"x","size":1}`))
	req.Header.Set("Content-Type", "application/json")
	e.authorize(req, amalSession)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	if got := e.serve(req); got.code != http.StatusForbidden {
		t.Errorf("a cross-origin write: %d %s, want 403", got.code, got.body)
	}
}

// No route reads a body over 1 MiB, whether its length is declared or not, even mounted
// without the console's entry point and its cap.
func TestNoRouteTakesABodyOverOneMiB(t *testing.T) {
	e := newEnv(t, true)
	e.insert("a.txt", "text/plain", "hello")
	big := bytes.Repeat([]byte(" "), maxBody+1)
	for _, r := range e.routes {
		req := httptest.NewRequest(r.method, concrete(r), bytes.NewReader(big))
		req.Header.Set("Content-Type", "application/json")
		e.authorize(req, amalSession)
		if got := e.serve(req); got.code != http.StatusRequestEntityTooLarge {
			t.Errorf("%s %s with %d bytes: %d %s, want 413", r.method, r.pattern, len(big), got.code, got.body)
		}
	}
	// Of unknown length: the routes that read a body stop at the cap.
	for _, r := range []route{
		{"POST", "/console/v1/projects/{project}/storage/uploads"},
		{"PATCH", "/console/v1/projects/{project}/storage/object"},
		{"POST", "/console/v1/projects/{project}/storage/object:deleteToken"},
		{"PUT", "/console/v1/projects/{project}/storage/cors"},
		{"PUT", "/console/v1/projects/{project}/storage/rules"},
		{"POST", "/console/v1/projects/{project}/storage/rules:validate"},
	} {
		body := `{"source":"` + strings.Repeat("a", maxBody) + `"}`
		req := httptest.NewRequest(r.method, concrete(r), io.MultiReader(strings.NewReader(body)))
		req.ContentLength = -1
		req.Header.Set("Content-Type", "application/json")
		e.authorize(req, amalSession)
		if got := e.serve(req); got.code != http.StatusRequestEntityTooLarge {
			t.Errorf("%s %s chunked past the cap: %d %s, want 413", r.method, r.pattern, got.code, got.body)
		}
	}
	// Just under the cap is read.
	body := `{"source":"` + strings.Repeat(" ", maxBody-20) + `"}`
	if got := e.do(amalSession, "POST", path("/rules:validate"), body); got.code != http.StatusOK {
		t.Errorf("a body under the cap: %d %s", got.code, got.body)
	}
}

// With Storage disabled the routes exist: the info route says so, and the others answer
// FAILED_PRECONDITION, after the same authentication.
func TestStorageDisabled(t *testing.T) {
	e := newEnv(t, false)
	got := e.do(amalSession, "GET", path(""), nil)
	if got.code != http.StatusOK || got.json["enabled"] != false || got.json["bucket"] != "chat-app.aruma.test" {
		t.Errorf("info: %d %s", got.code, got.body)
	}
	for _, r := range e.routes {
		if r.pattern == "/console/v1/projects/{project}/storage" {
			continue
		}
		if got := e.do(amalSession, r.method, concrete(r), nil); got.status() != "FAILED_PRECONDITION" {
			t.Errorf("%s %s disabled: %d %s", r.method, r.pattern, got.code, got.body)
		}
		if got := e.do(nobody, r.method, concrete(r), nil); got.code != http.StatusUnauthorized {
			t.Errorf("%s %s disabled, without credentials: %d", r.method, r.pattern, got.code)
		}
	}
}

func TestInfo(t *testing.T) {
	e := newEnv(t, true)
	got := e.do(amalCLI, "GET", path(""), nil)
	lim, _ := got.json["limits"].(map[string]any)
	if got.code != http.StatusOK || got.json["enabled"] != true || lim["uploadSessionSeconds"] != float64(3600) ||
		lim["requestBytes"] != float64(1<<20) {
		t.Errorf("info: %d %s", got.code, got.body)
	}
}
