package acceptance

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// While the server cannot reach Postgres, Auth's client calls (the SDKs' Identity Toolkit and
// Secure Token requests) answer 503 with a body that is not JSON, as an HTTP front answers for a
// backend that is down: the web SDK takes it for a network failure (auth/network-request-
// failed), keeps the signed-in user it persisted and retries, where a JSON error would sign the
// user out at the next app start (test/e2e/web-auth outage.spec.mjs drives the SDK itself). The
// Admin API keeps its google.rpc.Status UNAVAILABLE, which the Admin SDKs retry.
func TestAuthOutageLooksLikeANetworkFailureToClients(t *testing.T) {
	e := newEnv(t, nil)
	p := e.authProject("chat-app")
	up := ok(t, p.call(itk+"accounts:signUp", map[string]any{"email": "u@example.com", "password": "secret1", "returnSecureToken": true}))
	refresh, idToken := up.str("refreshToken"), up.str("idToken")
	_, key := p.owner.serviceAccountKey("chat-app", "backend")
	sat, _, err := exchange(t, key)
	if err != nil {
		t.Fatal(err)
	}

	e.Pool.Close() // Postgres is gone

	send := func(method, path, ctype, body, bearer string) (int, http.Header, string) {
		req, _ := http.NewRequest(method, e.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", ctype)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header, string(b)
	}
	clients := []struct{ name, path, ctype, body string }{
		{"signInWithPassword", itk + "accounts:signInWithPassword?key=" + p.key, "application/json", `{"email":"u@example.com","password":"secret1","returnSecureToken":true}`},
		{"signUp", itk + "accounts:signUp?key=" + p.key, "application/json", `{"returnSecureToken":true}`},
		{"lookup", itk + "accounts:lookup?key=" + p.key, "application/json", `{"idToken":"` + idToken + `"}`},
		{"lookup (iOS v3)", "/www.googleapis.com/identitytoolkit/v3/relyingparty/getAccountInfo?key=" + p.key, "application/json", `{"idToken":"` + idToken + `"}`},
		{"token refresh", sts + "?key=" + p.key, "application/x-www-form-urlencoded", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}}.Encode()},
	}
	for _, c := range clients {
		code, h, body := send("POST", c.path, c.ctype, c.body, "")
		if code != http.StatusServiceUnavailable || json.Valid([]byte(body)) || !strings.HasPrefix(h.Get("Content-Type"), "text/plain") {
			t.Errorf("%s while the database is down: %d %s %q, want 503 with a body that is not JSON", c.name, code, h.Get("Content-Type"), body)
		}
	}
	code, _, body := send("POST", itk+"projects/chat-app/accounts:lookup", "application/json", `{"localId":["x"]}`, sat)
	var status struct {
		Error struct{ Status string } `json:"error"`
	}
	if code != http.StatusServiceUnavailable || json.Unmarshal([]byte(body), &status) != nil || status.Error.Status != "UNAVAILABLE" {
		t.Errorf("Admin API while the database is down: %d %s, want 503 UNAVAILABLE in JSON", code, body)
	}
}

// TestCutRequestBodyIsNotAnOutage: a client that announces a body and goes away in the middle
// of it is answered 400, not 503: its request ends with the unexpected EOF a lost database
// connection ends with, and must not be taken for one.
func TestCutRequestBodyIsNotAnOutage(t *testing.T) {
	e := newEnv(t, nil)
	p := e.authProject("chat-app")
	u, err := url.Parse(e.URL)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		itk + "accounts:signUp?key=" + p.key,
		"/securetoken.googleapis.com/v1/token?key=" + p.key,
	} {
		conn, err := net.Dial("tcp", u.Host)
		if err != nil {
			t.Fatal(err)
		}
		body := `{"email":"u@example.com",`
		fmt.Fprintf(conn, "POST %s HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
			path, u.Host, len(body)+400, body)
		_ = conn.(*net.TCPConn).CloseWrite()
		_ = conn.SetReadDeadline(time.Now().Add(20 * time.Second))
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Errorf("%s: no answer: %v", path, err)
			conn.Close()
			continue
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		conn.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s with a cut body: %d %s, want 400", path, resp.StatusCode, b)
		}
	}
}
