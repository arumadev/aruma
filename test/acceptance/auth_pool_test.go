package acceptance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestSignInsOnAOneConnectionPool: a sign-in signs its ID token inside its transaction, and
// the first token of a project (after a restart or a rotation too) reads, or creates, the
// signing key. That read once went to the pool while the transaction held a connection, so as
// many concurrent first sign-ins as the pool has connections waited on each other until their
// clients gave up (tools/bench flaked this way on CI's 4-connection pool). On a one-connection
// pool a single sign-in shows it. Each step rotates the key first, which drops the cached
// signer, so every path starts cold.
func TestSignInsOnAOneConnectionPool(t *testing.T) {
	x := newIdPEnvWithPool(t, 1)
	p, e := x.authProject, x.env
	p.enableAnonymous()
	_, keyFile := p.owner.serviceAccountKey("chat-app", "backend")

	// A hung sign-in must fail the test, not hang the suite: the timeout closes the connection,
	// which cancels the request and lets the server shut down.
	hc := &http.Client{Timeout: 20 * time.Second}
	post := func(step, path, contentType, body string) *Response {
		t.Helper()
		resp, err := hc.Post(e.URL+path+"?key="+p.key, contentType, strings.NewReader(body))
		if err != nil {
			t.Fatalf("%s: %v", step, err)
		}
		r := decodeResponse(t, resp)
		if r.Status != http.StatusOK {
			t.Fatalf("%s: status %d: %s", step, r.Status, r.Body)
		}
		return r
	}
	call := func(step, method string, body map[string]any) *Response {
		t.Helper()
		b, _ := json.Marshal(body)
		return post(step, itk+"accounts:"+method, "application/json", string(b))
	}
	rotate := func() {
		t.Helper()
		if err := e.App.Auth.RotateSigningKey(context.Background(), p.ref(), "id_token", false); err != nil {
			t.Fatal(err)
		}
	}

	call("sign-up", "signUp", map[string]any{"email": "sara@example.com", "password": "secret-1", "returnSecureToken": true})
	rotate()
	in := call("password sign-in", "signInWithPassword", map[string]any{"email": "sara@example.com", "password": "secret-1", "returnSecureToken": true})
	rotate()
	call("password change", "update", map[string]any{"idToken": in.str("idToken"), "password": "secret-2", "returnSecureToken": true})
	rotate()
	anon := call("anonymous sign-up", "signUp", map[string]any{"returnSecureToken": true})
	rotate()
	upgraded := call("anonymous upgrade", "signUp", map[string]any{"idToken": anon.str("idToken"), "email": "omar@example.com", "password": "secret-1", "returnSecureToken": true})
	rotate()
	post("refresh", sts, "application/x-www-form-urlencoded",
		url.Values{"grant_type": {"refresh_token"}, "refresh_token": {upgraded.str("refreshToken")}}.Encode())
	rotate()
	custom := call("custom token sign-in", "signInWithCustomToken", map[string]any{"token": customToken(t, keyFile, "ct-1", nil, nil), "returnSecureToken": true})
	idp := func(step, sub, idToken string) {
		t.Helper()
		tok := x.google.idToken(googleClient, googleClaims(sub, sub+"@gmail.com", true))
		body := map[string]any{"requestUri": "http://localhost", "postBody": url.Values{"id_token": {tok}, "providerId": {"google.com"}}.Encode(), "returnSecureToken": true}
		if idToken != "" {
			body["idToken"] = idToken
		}
		call(step, "signInWithIdp", body)
	}
	rotate()
	idp("IdP sign-in", "g-1", "")
	rotate()
	idp("IdP link", "g-2", custom.str("idToken"))
}
