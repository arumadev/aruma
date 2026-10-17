package acceptance

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
)

// AC-1, AC-4: the setup code is logged once, works once, and the setup page is gone afterwards.
func TestFirstBootSetup(t *testing.T) {
	e := newEnv(t, nil)
	c := e.client()

	st := c.mustOK(c.do("GET", "/console/v1/status", nil))
	if st.JSON["setupRequired"] != true || st.JSON["signupOpen"] != false {
		t.Fatalf("status before setup: %s", st.Body)
	}
	code := e.setupCode()
	if n := strings.Count(e.Logs.String(), code); n != 1 {
		t.Fatalf("setup code appears %d times in the log, want 1", n)
	}

	body := func(code string) map[string]any {
		return map[string]any{"setupCode": code, "email": "owner@example.com", "password": testPassword}
	}
	expect(t, c.do("POST", "/console/v1/setup", body("")), 403, "PERMISSION_DENIED")
	expect(t, c.do("POST", "/console/v1/setup", body("wrong-code")), 403, "PERMISSION_DENIED")

	r := c.mustOK(c.do("POST", "/console/v1/setup", body(code)))
	if r.str("account", "email") != "owner@example.com" || r.str("csrfToken") == "" {
		t.Fatalf("setup response: %s", r.Body)
	}
	ck := r.Header.Get("Set-Cookie")
	for _, attr := range []string{"__Host-aruma_session=", "Path=/", "HttpOnly", "Secure", "SameSite=Lax"} {
		if !strings.Contains(ck, attr) {
			t.Errorf("session cookie lacks %q: %s", attr, ck)
		}
	}
	c.mustOK(c.do("GET", "/console/v1/me", nil))

	// The code cannot be used twice, and a restart issues no new one.
	expect(t, e.client().do("POST", "/console/v1/setup", body(code)), 403, "PERMISSION_DENIED")
	if _, required, _ := e.App.Service.PrepareSetup(context.Background()); required {
		t.Fatal("setup still required after the first account")
	}
	if st := c.do("GET", "/console/v1/status", nil); st.JSON["setupRequired"] != false {
		t.Fatalf("status after setup: %s", st.Body)
	}
}

// AC-5, ADR-010 S-7: unknown email, wrong password and disabled account look identical.
func TestLoginFailuresAreUniform(t *testing.T) {
	e := newEnv(t, nil)
	e.owner()
	e.account("disabled@example.com")
	if err := e.App.Service.SetDisabled(context.Background(), "disabled@example.com", true); err != nil {
		t.Fatal(err)
	}
	attempts := map[string][2]string{
		"unknown":  {"nobody@example.com", testPassword},
		"wrong":    {"owner@example.com", "wrong password!"},
		"disabled": {"disabled@example.com", testPassword},
	}
	var first string
	for name, cred := range attempts {
		r := e.client().do("POST", "/console/v1/auth:login", map[string]any{"email": cred[0], "password": cred[1]})
		expect(t, r, 401, "UNAUTHENTICATED")
		if r.Header.Get("Set-Cookie") != "" {
			t.Errorf("%s: failed login set a cookie", name)
		}
		if first == "" {
			first = string(r.Body)
		} else if string(r.Body) != first {
			t.Errorf("%s: response differs:\n%s\n%s", name, r.Body, first)
		}
	}
	// Email matching is case-insensitive.
	e.client().login("OWNER@Example.com", testPassword)
}

// ADR-010 S-6: repeated failures for one email are throttled.
func TestLoginBackoff(t *testing.T) {
	e := newEnv(t, nil)
	e.owner()
	c := e.client()
	for i := 0; i < 5; i++ {
		expect(t, c.do("POST", "/console/v1/auth:login", map[string]any{"email": "owner@example.com", "password": "nope-nope"}), 401, "")
	}
	r := c.do("POST", "/console/v1/auth:login", map[string]any{"email": "owner@example.com", "password": testPassword})
	expect(t, r, 429, "RESOURCE_EXHAUSTED")
	if r.Header.Get("Retry-After") == "" {
		t.Fatal("429 without Retry-After")
	}
}

// ADR-010 S-6: the per-IP bucket caps credential attempts across emails.
func TestLoginIPLimit(t *testing.T) {
	e := newEnv(t, nil)
	c := e.client()
	limited := false
	for i := 0; i < 15 && !limited; i++ {
		r := c.do("POST", "/console/v1/auth:login", map[string]any{"email": "u" + string(rune('a'+i)) + "@example.com", "password": "whatever-pw"})
		limited = r.Status == http.StatusTooManyRequests
	}
	if !limited {
		t.Fatal("15 attempts from one address were never throttled")
	}
}

// AC-6: changing the password signs out every other session, keeps the current one.
func TestChangePasswordRevokesOtherSessions(t *testing.T) {
	e := newEnv(t, nil)
	a := e.owner()
	b := e.client()
	b.login("owner@example.com", testPassword)

	expect(t, a.do("POST", "/console/v1/me:changePassword", map[string]any{"currentPassword": "wrong-one!", "newPassword": "brand new password"}), 400, "INVALID_ARGUMENT")
	a.mustOK(a.do("POST", "/console/v1/me:changePassword", map[string]any{"currentPassword": testPassword, "newPassword": "brand new password"}))

	a.mustOK(a.do("GET", "/console/v1/me", nil))
	expect(t, b.do("GET", "/console/v1/me", nil), 401, "UNAUTHENTICATED")
	expect(t, e.client().do("POST", "/console/v1/auth:login", map[string]any{"email": "owner@example.com", "password": testPassword}), 401, "")
	e.client().login("owner@example.com", "brand new password")
}

// AC-7: CLI tokens work as bearer credentials until revoked, then stop at once.
func TestAccessTokens(t *testing.T) {
	e := newEnv(t, nil)
	c := e.owner()
	created := c.mustOK(c.do("POST", "/console/v1/me/tokens", map[string]any{"name": "laptop", "expireDays": 30}))
	token := created.str("token")
	if !strings.HasPrefix(token, "aruma_pat_") || !strings.HasPrefix(token, created.str("prefix")) {
		t.Fatalf("created token: %s", created.Body)
	}
	list := c.mustOK(c.do("GET", "/console/v1/me/tokens", nil))
	if strings.Contains(string(list.Body), token) {
		t.Fatal("token plaintext returned again after creation")
	}

	cli := e.withBearer(token)
	if r := cli.mustOK(cli.do("GET", "/console/v1/me", nil)); r.str("account", "email") != "owner@example.com" {
		t.Fatalf("me via token: %s", r.Body)
	}
	// Account security operations need the session, not a CLI token.
	expect(t, cli.do("POST", "/console/v1/me/tokens", map[string]any{"name": "escalate"}), 401, "UNAUTHENTICATED")
	expect(t, cli.do("POST", "/console/v1/me:changePassword", map[string]any{"currentPassword": testPassword, "newPassword": "x-x-x-x-x-x"}), 401, "")

	c.mustOK(c.do("DELETE", "/console/v1/me/tokens/"+created.str("id"), nil))
	expect(t, cli.do("GET", "/console/v1/me", nil), 401, "UNAUTHENTICATED")
	expect(t, c.do("DELETE", "/console/v1/me/tokens/"+created.str("id"), nil), 404, "NOT_FOUND")
}

// AC-20: sign-up is refused while closed, and works once the operator opens it.
func TestSignupClosedThenOpen(t *testing.T) {
	body := map[string]any{"email": "new@example.com", "password": testPassword}
	closed := newEnv(t, nil)
	closed.owner()
	expect(t, closed.client().do("POST", "/console/v1/auth:signup", body), 403, "PERMISSION_DENIED")

	open := newEnv(t, map[string]string{"ARUMA_SIGNUP_OPEN": "true"})
	open.owner()
	c := open.client()
	c.mustOK(c.do("POST", "/console/v1/auth:signup", body))
	c.mustOK(c.do("GET", "/console/v1/me", nil))
	expect(t, open.client().do("POST", "/console/v1/auth:signup", body), 409, "ALREADY_EXISTS")
}

// ADR-010 S-5: cookie writes need the session's CSRF token and a same-origin request.
func TestCSRF(t *testing.T) {
	e := newEnv(t, nil)
	c := e.owner()
	other := e.client()
	other.login("owner@example.com", testPassword)

	csrf := c.csrf
	c.csrf = ""
	expect(t, c.do("PATCH", "/console/v1/me", map[string]any{"displayName": "x"}), 403, "PERMISSION_DENIED")
	c.csrf = other.csrf
	expect(t, c.do("PATCH", "/console/v1/me", map[string]any{"displayName": "x"}), 403, "PERMISSION_DENIED")
	c.csrf = csrf

	c.header["Sec-Fetch-Site"] = "cross-site"
	expect(t, c.do("PATCH", "/console/v1/me", map[string]any{"displayName": "x"}), 403, "PERMISSION_DENIED")
	delete(c.header, "Sec-Fetch-Site")
	c.header["Origin"] = "https://evil.example"
	expect(t, c.do("PATCH", "/console/v1/me", map[string]any{"displayName": "x"}), 403, "PERMISSION_DENIED")
	delete(c.header, "Origin")

	r := c.mustOK(c.do("PATCH", "/console/v1/me", map[string]any{"displayName": "Renamed", "locale": "en"}))
	if r.str("account", "displayName") != "Renamed" || r.str("account", "locale") != "en" {
		t.Fatalf("update: %s", r.Body)
	}
	// A reloaded page recovers the CSRF token from /me.
	fresh := e.client()
	fresh.cookies = c.cookies
	if fresh.mustOK(fresh.do("GET", "/console/v1/me", nil)).str("csrfToken") != csrf {
		t.Fatal("/me did not return the session's CSRF token")
	}
}

// ADR-010 S-4: every console route except the public ones needs credentials.
func TestEveryConsoleRouteRequiresAuth(t *testing.T) {
	e := newEnv(t, nil)
	e.owner()
	anon := e.client()
	checked := 0
	for _, rt := range e.App.Console.Routes() {
		if rt.Public {
			continue
		}
		path := fillPattern(rt.Pattern)
		r := anon.do(rt.Method, path, map[string]any{})
		if r.Status != 401 {
			t.Errorf("%s %s without credentials: %d", rt.Method, path, r.Status)
		}
		checked++
	}
	if checked < 8 {
		t.Fatalf("only %d protected routes found", checked)
	}
}

// fillPattern replaces wildcards with plausible values.
func fillPattern(p string) string {
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		if p[i] != '{' {
			b.WriteByte(p[i])
			continue
		}
		j := strings.IndexByte(p[i:], '}')
		b.WriteString("00000000-0000-7000-8000-000000000000")
		i += j
	}
	return b.String()
}

// AC-28: the emulator owner token and unsigned JWTs never authenticate.
func TestOwnerAndAlgNoneRefused(t *testing.T) {
	e := newEnv(t, nil)
	e.owner()
	none := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." +
		base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"x"}`)) + "."
	for _, tok := range []string{"owner", none} {
		expect(t, e.withBearer(tok).do("GET", "/console/v1/me", nil), 401, "UNAUTHENTICATED")
	}
}

// Sessions end at their idle expiry, and logout ends them at once.
func TestSessionExpiryAndLogout(t *testing.T) {
	e := newEnv(t, nil)
	c := e.owner()
	if _, err := e.Pool.Exec(context.Background(), "UPDATE control.sessions SET idle_expires_at = now() - interval '1 second'"); err != nil {
		t.Fatal(err)
	}
	expect(t, c.do("GET", "/console/v1/me", nil), 401, "UNAUTHENTICATED")

	c.login("owner@example.com", testPassword)
	sessions := c.mustOK(c.do("GET", "/console/v1/me/sessions", nil)).list("sessions")
	if len(sessions) != 1 || sessions[0]["current"] != true {
		t.Fatalf("sessions: %v", sessions)
	}
	saved := c.cookies
	c.mustOK(c.do("POST", "/console/v1/auth:logout", nil))
	replay := e.client()
	replay.cookies = saved
	expect(t, replay.do("GET", "/console/v1/me", nil), 401, "UNAUTHENTICATED")
}
