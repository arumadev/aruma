package acceptance

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/arumadev/aruma/internal/services/auth/tokens"
)

// Hardening: defensive review of credential validation across every service. The existing
// tamper suite (auth_tamper_test.go) puts forged ID tokens to the Auth service and the two
// Admin libraries; these tests put the same families of tokens to each service that accepts an
// ID token (Realtime Database over REST and WebSocket, Cloud Storage, the Document Database),
// and every credential of one project to every route of another.

// idTokenVariants returns, for the target project, tokens that must not be taken for a signed-in
// user of it, and one that must (the control). Every token with a valid signature is signed with
// the project's real key, so the claim checks themselves are what is tested.
func idTokenVariants(t *testing.T, h *hardEnv, target, other *hardProject) (control map[string]string, refused map[string]string) {
	t.Helper()
	ap := &authProject{env: h.Env, owner: target.owner, id: target.id, number: target.number, key: target.key}
	hdr, payload, _ := jwtParts(t, target.idToken)
	kid, key := ap.signingKey(tokens.IDToken)
	claimsWith := func(edit func(map[string]any)) map[string]any {
		c := map[string]any{}
		for k, v := range payload {
			c[k] = v
		}
		if edit != nil {
			edit(c)
		}
		return c
	}
	resign := func(edit func(map[string]any)) string {
		return rsaSign(t, key, map[string]any{"alg": "RS256", "kid": kid, "typ": "JWT"}, claimsWith(edit))
	}
	now := time.Now()
	_, otherPayload, _ := jwtParts(t, other.idToken)

	control = map[string]string{
		"genuine":                          target.idToken,
		"re-signed with the project's key": resign(nil),
	}
	refused = map[string]string{
		"aud is another project's id":  resign(func(c map[string]any) { c["aud"] = other.id }),
		"aud is the project number":    resign(func(c map[string]any) { c["aud"] = target.number }),
		"aud missing":                  resign(func(c map[string]any) { delete(c, "aud") }),
		"iss is another project's":     resign(func(c map[string]any) { c["iss"] = otherPayload["iss"] }),
		"iss is Google's securetoken":  resign(func(c map[string]any) { c["iss"] = "https://securetoken.google.com/" + target.id }),
		"iss missing":                  resign(func(c map[string]any) { delete(c, "iss") }),
		"expired an hour ago":          resign(func(c map[string]any) { c["iat"], c["exp"] = now.Add(-2*time.Hour).Unix(), now.Add(-time.Hour).Unix() }),
		"exp missing":                  resign(func(c map[string]any) { delete(c, "exp") }),
		"exp zero":                     resign(func(c map[string]any) { c["exp"] = 0 }),
		"issued in the future":         resign(func(c map[string]any) { c["iat"], c["exp"] = now.Add(time.Hour).Unix(), now.Add(2*time.Hour).Unix() }),
		"auth_time missing":            resign(func(c map[string]any) { delete(c, "auth_time") }),
		"auth_time in the future":      resign(func(c map[string]any) { c["auth_time"] = now.Add(time.Hour).Unix() }),
		"sub missing":                  resign(func(c map[string]any) { delete(c, "sub") }),
		"sub empty":                    resign(func(c map[string]any) { c["sub"] = "" }),
		"sub over 128 characters":      resign(func(c map[string]any) { c["sub"] = strings.Repeat("a", 129) }),
		"sub of a user that never was": resign(func(c map[string]any) { c["sub"] = "ghost" }),
		"user created after the token": resign(func(c map[string]any) {
			c["iat"], c["auth_time"] = now.Add(-48*time.Hour).Unix(), now.Add(-48*time.Hour).Unix()
		}),
		"signed with the other project's key": func() string {
			oap := &authProject{env: h.Env, owner: other.owner, id: other.id, number: other.number, key: other.key}
			okid, okey := oap.signingKey(tokens.IDToken)
			return rsaSign(t, okey, map[string]any{"alg": "RS256", "kid": okid, "typ": "JWT"}, claimsWith(nil))
		}(),
		"other project's genuine token":        other.idToken,
		"signed with an unknown key, real kid": rsaSign(t, newRSAKey(t), map[string]any{"alg": "RS256", "kid": kid, "typ": "JWT"}, claimsWith(nil)),
		"signed with an unknown key, no kid":   rsaSign(t, newRSAKey(t), map[string]any{"alg": "RS256", "typ": "JWT"}, claimsWith(nil)),
		"alg none":                             segment(map[string]any{"alg": "none", "kid": kid, "typ": "JWT"}) + "." + segment(payload) + ".",
		"alg HS256 keyed with the kid":         hs256(segment(map[string]any{"alg": "HS256", "kid": kid, "typ": "JWT"})+"."+segment(payload), []byte(kid)),
		"signature of another token":           strings.Join([]string{segment(hdr), segment(claimsWith(func(c map[string]any) { c["sub"] = "someone" })), strings.Split(target.idToken, ".")[2]}, "."),
		"the refresh token":                    target.refresh,
		"the project's API key":                target.key,
		"two segments":                         segment(hdr) + "." + segment(payload),
		"empty":                                "",
		"garbage":                              "not a token",
	}
	return control, refused
}

// Every service that takes an ID token refuses each way of presenting a token that is not a
// genuine, current one of the project the request names.
func TestHardIDTokenRefusedOnEveryService(t *testing.T) {
	h := newHardEnv(t, nil)
	h.open(h.a, "alpha-secret")
	h.open(h.b, "bravo-secret")
	control, refused := idTokenVariants(t, h, h.b, h.a)
	for _, s := range hardSurfaces() {
		t.Run(s.name, func(t *testing.T) {
			for name, tok := range control {
				if got := s.read(h, h.b, tok); got != h.b.marker {
					t.Errorf("control %q: served %q, want %q", name, got, h.b.marker)
				}
			}
			for name, tok := range refused {
				if got := s.read(h, h.b, tok); got != "" {
					t.Errorf("%s: a forged token was accepted and served %q", name, got)
				}
			}
		})
	}
}

// A signed-in user, a service account and an owner of project A reach nothing of project B on
// any service, by project id or by number, and each reaches its own project.
func TestHardForeignProjectCredentialsRefusedEverywhere(t *testing.T) {
	h := newHardEnv(t, nil)
	h.open(h.a, "alpha-secret")
	h.open(h.b, "bravo-secret")
	for _, s := range hardAdminSurfaces() {
		t.Run(s.name, func(t *testing.T) {
			for _, c := range []struct {
				kind  string
				want  bool
				token func(*hardProject) string
			}{{"service account token", s.sat, func(p *hardProject) string { return p.sat }}, {"CLI token", s.pat, func(p *hardProject) string { return p.pat }}} {
				own := h.b
				if got := s.read(h, own, c.token(own)); (got == own.marker) != c.want {
					t.Errorf("own %s: served %q, accepted should be %v", c.kind, got, c.want)
				}
				if got := s.read(h, h.b, c.token(h.a)); got != "" {
					t.Errorf("project A's %s served project B's data: %q", c.kind, got)
				}
				if got := s.read(h, h.a, c.token(h.b)); got != "" {
					t.Errorf("project B's %s served project A's data: %q", c.kind, got)
				}
			}
		})
	}
	for _, s := range hardSurfaces() {
		t.Run(s.name+" with a foreign ID token", func(t *testing.T) {
			if got := s.read(h, h.b, h.a.idToken); got != "" {
				t.Errorf("project A's user read project B: %q", got)
			}
			if got := s.read(h, h.a, h.b.idToken); got != "" {
				t.Errorf("project B's user read project A: %q", got)
			}
		})
	}
}

// Credentials of one type are not taken for another's: a token an API does not list is refused
// there, even when it is a perfectly good one elsewhere.
func TestHardCredentialTypeConfusion(t *testing.T) {
	h := newHardEnv(t, nil)
	h.open(h.b, "bravo-secret")
	// An end user's ID token, a refresh token, an API key and the console's session cookie are
	// not admin credentials anywhere.
	cookie := ""
	for _, ck := range h.b.owner.cookies {
		cookie = ck.Value
	}
	if cookie == "" {
		t.Fatal("no console session cookie")
	}
	for name, tok := range map[string]string{"ID token": h.b.idToken, "refresh token": h.b.refresh, "API key": h.b.key, "session cookie": cookie} {
		for _, s := range hardAdminSurfaces() {
			// The WebSocket's auth message and the Document Database take an ID token too: there
			// it is a user, whom the open rules serve.
			if name == "ID token" && (s.name == "rtdb websocket auth" || s.name == "documents rest") {
				continue
			}
			if got := s.read(h, h.b, tok); got != "" {
				t.Errorf("%s as a bearer on %s served %q", name, s.name, got)
			}
		}
	}
	// A service account's access token and an owner's CLI token are not an end user's identity:
	// presented as an ID token they are refused, not taken for a user.
	for name, tok := range map[string]string{"service account token": h.b.sat, "CLI token": h.b.pat} {
		for _, s := range hardSurfaces() {
			// The same two take an admin credential in the same slot, and serve it as an admin.
			if s.name == "rtdb websocket auth" || s.name == "documents rest bearer" {
				continue
			}
			if got := s.read(h, h.b, tok); got != "" {
				t.Errorf("%s as an ID token on %s served %q", name, s.name, got)
			}
		}
	}
}

func hs256(signing string, secret []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(signing))
	return signing + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// A refresh token is bound to its project and its user, is not forgiving of an edit, and is not
// exchangeable at the wrong endpoint, where the owner's CLI flow and a user's flow stay apart.
func TestHardRefreshTokensAreProjectBoundAndStrict(t *testing.T) {
	h := newHardEnv(t, nil)
	a, b := h.a, h.b
	refresh := func(p *hardProject, token string) *Response {
		ap := &authProject{env: h.Env, key: p.key}
		return ap.refresh(token)
	}
	if r := refresh(b, b.refresh); r.Status != 200 || r.str("id_token") == "" {
		t.Fatalf("control: %d %s", r.Status, r.Body)
	}
	flip := func(s string) string {
		i := len(s) / 2
		c := byte('A')
		if s[i] == 'A' {
			c = 'B'
		}
		return s[:i] + string(c) + s[i+1:]
	}
	for name, tok := range map[string]string{
		"another project's token": a.refresh,
		"one character changed":   flip(b.refresh),
		"truncated":               b.refresh[:len(b.refresh)-1],
		"extended":                b.refresh + "A",
		"surrounded by space":     " " + b.refresh + " ",
		"lower cased":             strings.ToLower(b.refresh),
		"empty":                   "",
		"an ID token":             b.idToken,
		"a service account token": b.sat,
		"a CLI token":             b.pat,
		"the API key":             b.key,
		"two tokens":              b.refresh + "," + a.refresh,
		"null byte":               b.refresh + "\x00",
	} {
		if r := refresh(b, tok); r.Status == 200 {
			t.Errorf("%s was exchanged: %s", name, r.Body)
		}
	}
	// A's token on B's key and B's on A's.
	if r := refresh(a, b.refresh); r.Status == 200 {
		t.Errorf("B's refresh token was exchanged under A's key: %s", r.Body)
	}
	// The Firebase CLI's grant at /oauth2/v3/token takes the owner's CLI token, not a user's.
	post := func(rt string) (int, string) {
		resp, err := http.PostForm(h.URL+"/oauth2/v3/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {rt}})
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		bd, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(bd)
	}
	for name, tok := range map[string]string{"a user's refresh token": b.refresh, "an ID token": b.idToken, "a service account token": b.sat, "the API key": b.key, "empty": "", "garbage": "x"} {
		if code, body := post(tok); code == 200 {
			t.Errorf("the CLI grant took %s: %s", name, body)
		}
	}
	// The user's refresh token still works afterwards: the refusals did not consume it.
	if r := refresh(b, b.refresh); r.Status != 200 {
		t.Errorf("the token stopped working after refusals: %s", r.Body)
	}
}
