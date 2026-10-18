package acceptance

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	identity "github.com/osama300/aurma-project/internal/identity/core"
	"github.com/osama300/aurma-project/internal/identity/queries"
	"github.com/osama300/aurma-project/internal/identity/tokens"
)

// Helpers shared by the Phase 2 error, parity and tamper suites.

// ref is the project as the Auth service names it, for direct service calls in test setup.
func (p *authProject) ref() identity.Project {
	p.env.t.Helper()
	n, err := strconv.ParseInt(p.number, 10, 64)
	if err != nil {
		p.env.t.Fatal(err)
	}
	return identity.Project{Number: n, ID: p.id}
}

// configure edits the project's sign-in settings through the Auth service, as the console's
// Sign-in methods screen will.
func (p *authProject) configure(fn func(*queries.AuthProjectConfig)) {
	p.env.t.Helper()
	if _, err := p.env.App.Auth.UpdateConfig(context.Background(), p.ref(), func(c *queries.AuthProjectConfig) error {
		fn(c)
		return nil
	}); err != nil {
		p.env.t.Fatal(err)
	}
}

// protection turns email enumeration protection on or off.
func (p *authProject) protection(on bool) {
	p.configure(func(c *queries.AuthProjectConfig) { c.EnumerationProtection = on })
}

// signUp creates an email/password user and returns its session.
func (p *authProject) signUp(email, password string) *Response {
	p.env.t.Helper()
	return ok(p.env.t, p.call(itk+"accounts:signUp", map[string]any{"email": email, "password": password, "returnSecureToken": true}))
}

// sql runs a statement against the server's database.
func (p *authProject) sql(stmt string, args ...any) {
	p.env.t.Helper()
	if _, err := p.env.Pool.Exec(context.Background(), stmt, args...); err != nil {
		p.env.t.Fatal(err)
	}
}

// secondProject creates another project with a web app under the same owner.
func (p *authProject) secondProject(id string) *authProject {
	p.env.t.Helper()
	q := &authProject{env: p.env, owner: p.owner, id: id}
	q.number = p.owner.project(id).str("projectNumber")
	web := p.owner.app(id, "WEB", "")
	q.key = p.owner.mustOK(p.owner.do("GET", "/console/v1/projects/"+id+"/apiKeys/"+web.str("apiKeyId"), nil)).str("keyString")
	return q
}

// signWithProjectKey signs claims with the project's real active ID token key. It forges
// tokens whose signature is valid, so the claim checks themselves are what is tested.
func (p *authProject) signWithProjectKey(claims map[string]any) string {
	p.env.t.Helper()
	tok, err := p.env.App.Auth.Tokens().Sign(context.Background(), p.ref().Number, tokens.IDToken, claims)
	if err != nil {
		p.env.t.Fatal(err)
	}
	return tok
}

// jwtParts splits a JWT into its decoded header and payload and its raw signature segment.
func jwtParts(t *testing.T, jwt string) (header, payload map[string]any, sig string) {
	t.Helper()
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("not a JWT: %q", jwt)
	}
	dec := func(s string) map[string]any {
		b, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	return dec(parts[0]), dec(parts[1]), parts[2]
}

func segment(v any) string {
	b, _ := json.Marshal(v)
	return base64.RawURLEncoding.EncodeToString(b)
}

// rsaSign signs header.payload with key as RS256.
func rsaSign(t *testing.T, key *rsa.PrivateKey, header, payload map[string]any) string {
	t.Helper()
	signing := segment(header) + "." + segment(payload)
	digest := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// newRSAKey returns a fresh 2048-bit key that Aruma has never seen.
func newRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// errCode returns the Identity Toolkit code of an error response: the message up to " : ".
func errCode(r *Response) string {
	code, _, _ := strings.Cut(r.str("error", "message"), " : ")
	return code
}
