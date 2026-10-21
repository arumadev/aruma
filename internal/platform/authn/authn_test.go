package authn

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/arumadev/aruma/internal/platform/apierr"
	"github.com/arumadev/aruma/internal/platform/crypto"
)

type fakeVerifier struct{ session, pat, sat string }

var errBad = apierr.New(apierr.Unauthenticated, "bad")

func (f fakeVerifier) VerifySession(_ context.Context, t string) (*Principal, error) {
	if t != f.session {
		return nil, errBad
	}
	return &Principal{Kind: KindAccount, Method: MethodSession, AccountID: uuid.New()}, nil
}

func (f fakeVerifier) VerifyAccessToken(_ context.Context, t string) (*Principal, error) {
	if t != f.pat {
		return nil, errBad
	}
	return &Principal{Kind: KindAccount, Method: MethodAccessToken}, nil
}

func (f fakeVerifier) VerifyServiceToken(_ context.Context, t string) (*Principal, error) {
	if t != f.sat {
		return nil, errBad
	}
	return &Principal{Kind: KindServiceAccount, Method: MethodServiceToken}, nil
}

func setup(t *testing.T) (*Authenticator, fakeVerifier, *bytes.Buffer) {
	keys, err := crypto.NewKeys(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	f := fakeVerifier{session: "sess-token", pat: AccessTokenPrefix + "abc", sat: ServiceTokenPrefix + "xyz"}
	var logs bytes.Buffer
	return &Authenticator{Verifier: f, Keys: keys, CookieName: "s", Logger: slog.New(slog.NewTextHandler(&logs, nil))}, f, &logs
}

func call(a *Authenticator, p Policy, method string, mutate func(*http.Request)) (int, string) {
	h := a.Require(p, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, FromContext(r.Context()).ActorType())
	}))
	r := httptest.NewRequest(method, "/x", nil)
	mutate(r)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec.Code, rec.Body.String()
}

func TestPolicies(t *testing.T) {
	a, f, _ := setup(t)
	console := Policy{Session: true, AccessToken: true}
	mgmt := Policy{AccessToken: true, ServiceToken: true}
	bearer := func(tok string) func(*http.Request) {
		return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) }
	}
	cookie := func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "s", Value: f.session}) }

	cases := []struct {
		name   string
		policy Policy
		method string
		mut    func(*http.Request)
		code   int
	}{
		{"no credentials", console, "GET", func(*http.Request) {}, 401},
		{"session GET", console, "GET", cookie, 200},
		{"session POST without CSRF", console, "POST", cookie, 403},
		{"session POST with CSRF", console, "POST", func(r *http.Request) {
			cookie(r)
			r.Header.Set(CSRFHeader, a.Keys.CSRFToken(f.session))
		}, 200},
		{"session POST with another session's CSRF", console, "POST", func(r *http.Request) {
			cookie(r)
			r.Header.Set(CSRFHeader, a.Keys.CSRFToken("other-session"))
		}, 403},
		{"PAT on console needs no CSRF", console, "POST", bearer(f.pat), 200},
		{"service token refused by console", console, "GET", bearer(f.sat), 401},
		{"session refused by mgmt", mgmt, "GET", cookie, 401},
		{"service token on mgmt", mgmt, "GET", bearer(f.sat), 200},
		{"wrong PAT", mgmt, "GET", bearer(AccessTokenPrefix + "nope"), 401},
		{"API key is not a credential", mgmt, "GET", bearer("AruQ7mX2kLp9Vt4nRb8sWc1yZd6fHg3jKe5aN0o"), 401},
		{"basic auth", mgmt, "GET", func(r *http.Request) { r.Header.Set("Authorization", "Basic Zm9vOmJhcg==") }, 401},
	}
	for _, c := range cases {
		if code, _ := call(a, c.policy, c.method, c.mut); code != c.code {
			t.Errorf("%s: %d, want %d", c.name, code, c.code)
		}
	}
}

// AC-28: the emulator owner token and unsigned JWTs are always refused, and the attempt logged.
func TestRejectsOwnerAndAlgNone(t *testing.T) {
	a, _, logs := setup(t)
	all := Policy{Session: true, AccessToken: true, ServiceToken: true}
	none := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`)) + "." +
		base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"admin"}`)) + "."
	for _, tok := range []string{"owner", none} {
		code, _ := call(a, all, "GET", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) })
		if code != 401 {
			t.Errorf("token %q: %d, want 401", tok, code)
		}
	}
	out := logs.String()
	if !strings.Contains(out, "rejected emulator owner token") || !strings.Contains(out, "rejected unsigned JWT") {
		t.Fatalf("attempts not logged:\n%s", out)
	}
}
