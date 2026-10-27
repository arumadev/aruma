package authn

import (
	"bytes"
	"context"
	"crypto/tls"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"log/slog"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"

	"github.com/arumadev/aruma/internal/platform/apierr"
)

// docsVerifier is a mutable Verifier for aruma_pat_ and aruma_sat_ tokens.
type docsVerifier struct {
	mu      sync.Mutex
	account uuid.UUID
	sa      uuid.UUID
	pat     string
	sat     string
	scopes  []string
}

func (f *docsVerifier) VerifySession(context.Context, string) (*Principal, error) {
	return nil, errInvalidCredentials
}

func (f *docsVerifier) VerifyAccessToken(_ context.Context, t string) (*Principal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t != f.pat {
		return nil, errInvalidCredentials
	}
	return &Principal{Kind: KindAccount, Method: MethodAccessToken, AccountID: f.account}, nil
}

func (f *docsVerifier) VerifyServiceToken(_ context.Context, t string) (*Principal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t != f.sat {
		return nil, errInvalidCredentials
	}
	return &Principal{Kind: KindServiceAccount, Method: MethodServiceToken, ServiceAccountID: f.sa,
		ProjectNumber: testProject, Scopes: f.scopes, ExpiresAt: time.Now().Add(time.Hour)}, nil
}

func (f *docsVerifier) revoke() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pat, f.sat = "revoked", "revoked"
}

type owners struct{ account uuid.UUID }

func (o owners) OwnsProject(_ context.Context, account uuid.UUID, project int64) (bool, error) {
	return account == o.account && project == testProject, nil
}

type docsFixture struct {
	d    *Documents
	dir  *fakeDirectory
	clk  *clock
	v    *docsVerifier
	logs *bytes.Buffer
}

func newDocs(t *testing.T, change func(*DocumentsOptions)) docsFixture {
	t.Helper()
	sav, dir, clk, logs := newTestVerifier(t)
	v := &docsVerifier{account: uuid.New(), sa: uuid.New(), pat: AccessTokenPrefix + "pat", sat: ServiceTokenPrefix + "sat",
		scopes: []string{ScopeDatastore}}
	opts := DocumentsOptions{
		Verifier: v, ServiceAccountJWTs: sav, Owners: owners{v.account}, PublicURL: testPublicURL,
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("127.0.0.1/32")},
		Logger:         slog.New(slog.NewTextHandler(logs, nil)),
	}
	if change != nil {
		change(&opts)
	}
	return docsFixture{d: NewDocuments(opts), dir: dir, clk: clk, v: v, logs: logs}
}

var (
	tlsConn       = Transport{TLS: true, Peer: netip.MustParseAddr("203.0.113.9")}
	plainStranger = Transport{Peer: netip.MustParseAddr("203.0.113.9")}
	proxyHTTPS    = Transport{Peer: netip.MustParseAddr("10.1.2.3"), ForwardedProto: "https"}
	proxyHTTP     = Transport{Peer: netip.MustParseAddr("10.1.2.3"), ForwardedProto: "http"}
	proxyNoHeader = Transport{Peer: netip.MustParseAddr("10.1.2.3")}
	loopbackPeer  = Transport{Peer: netip.MustParseAddr("127.0.0.1")}
	loopbackV6    = Transport{Peer: netip.MustParseAddr("::1")}
)

func idToken(iss string) string {
	return seg(map[string]any{"alg": "RS256", "kid": "k"}) + "." + seg(map[string]any{"iss": iss, "sub": "uid", "aud": "demo-project"}) + ".c2ln"
}

func TestDocumentsResolve(t *testing.T) {
	f := newDocs(t, nil)
	k := testKeys()
	goTok, nodeTok := goJWT(t, k[0], f.clk.now()), nodeJWT(t, k[0], f.clk.now())

	type want struct {
		code apierr.Code
		role Role
	}
	admin := want{apierr.OK, RoleAdmin}
	refused := want{code: apierr.Unauthenticated}
	cases := []struct {
		name string
		auth string
		tr   Transport
		want want
	}{
		{"no credential", "", plainStranger, want{apierr.OK, RoleAnonymous}},
		{"Go JWT over TLS", "Bearer " + goTok, tlsConn, admin},
		{"Node JWT over TLS", "Bearer " + nodeTok, tlsConn, admin},
		{"Node JWT via a trusted proxy with https", "Bearer " + nodeTok, proxyHTTPS, admin},
		{"aruma_sat_ over TLS", "Bearer " + f.v.sat, tlsConn, admin},
		{"aruma_pat_ over TLS", "bearer " + f.v.pat, tlsConn, admin},
		{"ID token in the clear", "Bearer " + idToken(testPublicURL+"/issuers/demo-project"), plainStranger, want{apierr.OK, RoleEndUser}},

		// §3.7: the plaintext rule, for each admin credential.
		{"JWT in the clear from a stranger", "Bearer " + goTok, plainStranger, refused},
		{"aruma_sat_ in the clear from a stranger", "Bearer " + f.v.sat, plainStranger, refused},
		{"aruma_pat_ in the clear from a stranger", "Bearer " + f.v.pat, plainStranger, refused},
		{"JWT via a trusted proxy without X-Forwarded-Proto", "Bearer " + goTok, proxyNoHeader, refused},
		{"JWT via a trusted proxy with http", "Bearer " + goTok, proxyHTTP, refused},
		{"JWT via a trusted proxy with a list", "Bearer " + goTok, Transport{Peer: proxyHTTPS.Peer, ForwardedProto: "http, https"}, refused},
		{"X-Forwarded-Proto from an untrusted peer", "Bearer " + goTok, Transport{Peer: plainStranger.Peer, ForwardedProto: "https"}, refused},
		{"loopback peer on a public server, no header", "Bearer " + goTok, Transport{Peer: netip.MustParseAddr("127.0.0.2")}, refused},
		{"no peer", "Bearer " + goTok, Transport{}, refused},

		// §6: owner is refused unless opted in.
		{"owner, not opted in", "Bearer owner", loopbackPeer, refused},
		// §2: issuers tell the JWTs apart.
		{"ID token whose iss is an email", "Bearer " + idToken(testEmail), tlsConn, refused},
		{"JWT of an unknown issuer", "Bearer " + idToken("https://securetoken.google.com/demo-project"), tlsConn, refused},
		{"ID token of the bare issuer prefix", "Bearer " + idToken(testPublicURL+"/issuers/"), tlsConn, refused},
		{"alg none", "Bearer " + seg(map[string]any{"alg": "none"}) + "." + seg(map[string]any{"iss": testEmail, "sub": testEmail}) + ".", tlsConn, refused},
		{"basic auth", "Basic Zm9vOmJhcg==", tlsConn, refused},
		{"empty bearer", "Bearer ", tlsConn, refused},
		{"unknown token", "Bearer AIzaSyFakeApiKey", tlsConn, refused},
		{"aruma_pat_ unknown", "Bearer " + AccessTokenPrefix + "nope", tlsConn, refused},
	}
	for _, c := range cases {
		caller, err := f.d.Resolve(t.Context(), c.auth, c.tr)
		if apierr.CodeOf(err) != c.want.code {
			t.Errorf("%s: %v, want %v", c.name, err, c.want.code)
			continue
		}
		if err == nil && caller.Role != c.want.role {
			t.Errorf("%s: role %d, want %d", c.name, caller.Role, c.want.role)
		}
	}

	out := f.logs.String()
	for _, w := range []string{"rejected emulator owner token", "rejected unsigned JWT", "rejected a credential sent without TLS", "client_ip=203.0.113.9"} {
		if !strings.Contains(out, w) {
			t.Errorf("log lacks %q:\n%s", w, out)
		}
	}
	for _, secret := range []string{goTok, f.v.sat, f.v.pat} {
		if strings.Contains(out, secret) {
			t.Error("a credential reached the log")
		}
	}

	_, err := f.d.Resolve(t.Context(), "Bearer "+goTok, plainStranger)
	if err == nil || !strings.Contains(err.Error(), "TLS") {
		t.Errorf("the plaintext refusal must say TLS is needed: %v", err)
	}
	_, err = f.d.Resolve(t.Context(), "Bearer owner", loopbackPeer)
	if err == nil || !strings.Contains(err.Error(), "documents.dev_owner_token") {
		t.Errorf("the owner refusal must say what to do: %v", err)
	}
}

func TestDocumentsResolveHTTP(t *testing.T) {
	f := newDocs(t, nil)
	tok := nodeJWT(t, testKeys()[0], f.clk.now())

	r := httptest.NewRequest("POST", "/v1/projects/demo-project/databases/(default)/documents:commit", nil)
	r.RemoteAddr = "10.9.9.9:41000"
	r.Header.Set("Authorization", "Bearer "+tok)
	if _, err := f.d.ResolveHTTP(r); apierr.CodeOf(err) != apierr.Unauthenticated {
		t.Fatalf("trusted proxy without X-Forwarded-Proto: %v", err)
	}
	r.Header.Set("X-Forwarded-Proto", "https")
	if c, err := f.d.ResolveHTTP(r); err != nil || c.Role != RoleAdmin {
		t.Fatalf("trusted proxy with https: %v", err)
	}
	r.RemoteAddr = "[::ffff:203.0.113.4]:41000"
	if _, err := f.d.ResolveHTTP(r); apierr.CodeOf(err) != apierr.Unauthenticated {
		t.Fatalf("untrusted peer claiming https: %v", err)
	}
	r.TLS = &tls.ConnectionState{}
	if _, err := f.d.ResolveHTTP(r); err != nil {
		t.Fatalf("TLS: %v", err)
	}
}

// A loopback deployment (public_url and listen loopback) accepts credentials in the clear from
// its own machine, and, opted in, the owner token from there alone.
func TestDocumentsLoopbackDeployment(t *testing.T) {
	k := testKeys()
	dev := newDocs(t, func(o *DocumentsOptions) { o.LoopbackDeployment = true; o.TrustedProxies = nil })
	if c, err := dev.d.Resolve(t.Context(), "Bearer "+goJWT(t, k[0], dev.clk.now()), loopbackPeer); err != nil || c.Role != RoleAdmin {
		t.Errorf("Go over h2c on a dev server: %v", err)
	}
	if _, err := dev.d.Resolve(t.Context(), "Bearer "+goJWT(t, k[0], dev.clk.now()), plainStranger); apierr.CodeOf(err) != apierr.Unauthenticated {
		t.Errorf("a stranger in the clear on a dev server: %v", err)
	}
	if _, err := dev.d.Resolve(t.Context(), "Bearer owner", loopbackPeer); apierr.CodeOf(err) != apierr.Unauthenticated {
		t.Errorf("owner on a dev server without the flag: %v", err)
	}

	opted := newDocs(t, func(o *DocumentsOptions) { o.LoopbackDeployment = true; o.DevOwnerToken = true })
	if !strings.Contains(opted.logs.String(), "dev_owner_token is on") {
		t.Error("no warning at start")
	}
	for _, tr := range []Transport{loopbackPeer, loopbackV6} {
		c, err := opted.d.Resolve(t.Context(), "Bearer owner", tr)
		if err != nil || c.Role != RoleAdmin || !c.IsDevOwner {
			t.Errorf("owner opted in from %v: %v", tr.Peer, err)
		}
	}
	if _, err := opted.d.Resolve(t.Context(), "Bearer owner", plainStranger); apierr.CodeOf(err) != apierr.Unauthenticated {
		t.Errorf("owner from another machine: %v", err)
	}
	if _, err := opted.d.Resolve(t.Context(), "Bearer owner", tlsConn); apierr.CodeOf(err) != apierr.Unauthenticated {
		t.Errorf("owner over TLS from another machine: %v", err)
	}

	// From a browser any page may be the sender, and the owner token is no secret: a request with
	// an Origin is refused it. A credential the page was given is accepted as from anyone.
	browser := Transport{Peer: loopbackPeer.Peer, Browser: true}
	if _, err := opted.d.Resolve(t.Context(), "Bearer owner", browser); err == nil || !strings.Contains(err.Error(), "documents.dev_owner_token") {
		t.Errorf("owner from a browser: %v", err)
	}
	if _, err := opted.d.Resolve(t.Context(), "Bearer owner", Transport{TLS: true, Peer: loopbackPeer.Peer, Browser: true}); apierr.CodeOf(err) != apierr.Unauthenticated {
		t.Errorf("owner over TLS from a browser: %v", err)
	}
	if c, err := opted.d.Resolve(t.Context(), "Bearer "+goJWT(t, k[0], opted.clk.now()), browser); err != nil || c.Role != RoleAdmin {
		t.Errorf("a credential in the clear from a browser: %v", err)
	}
	r := httptest.NewRequest("POST", "/v1/projects/demo-project/databases/(default)/documents:commit", nil)
	r.RemoteAddr = "127.0.0.1:41000"
	r.Header.Set("Authorization", "Bearer owner")
	if _, err := opted.d.ResolveHTTP(r); err != nil {
		t.Errorf("owner without an Origin: %v", err)
	}
	r.Header.Set("Origin", "https://evil.example")
	if _, err := opted.d.ResolveHTTP(r); apierr.CodeOf(err) != apierr.Unauthenticated {
		t.Errorf("owner with an Origin: %v", err)
	}

	// The flag without a loopback deployment (config refuses it) is ignored.
	public := newDocs(t, func(o *DocumentsOptions) { o.DevOwnerToken = true })
	if _, err := public.d.Resolve(t.Context(), "Bearer owner", loopbackPeer); apierr.CodeOf(err) != apierr.Unauthenticated {
		t.Errorf("owner on a public server through a local proxy: %v", err)
	}
}

func TestDocumentsServiceTokenScopes(t *testing.T) {
	f := newDocs(t, nil)
	f.v.scopes = nil
	if _, err := f.d.Resolve(t.Context(), "Bearer "+f.v.sat, tlsConn); apierr.CodeOf(err) != apierr.Unauthenticated {
		t.Errorf("aruma_sat_ without scopes: %v", err)
	}
	f.v.scopes = []string{"https://www.googleapis.com/auth/devstorage.read_only"}
	if _, err := f.d.Resolve(t.Context(), "Bearer "+f.v.sat, tlsConn); apierr.CodeOf(err) != apierr.Unauthenticated {
		t.Errorf("aruma_sat_ with a storage scope: %v", err)
	}
}

func TestDocumentsAdmit(t *testing.T) {
	f := newDocs(t, nil)
	resolve := func(auth string, tr Transport) *Caller {
		t.Helper()
		c, err := f.d.Resolve(t.Context(), auth, tr)
		if err != nil {
			t.Fatalf("%s: %v", auth, err)
		}
		return c
	}
	jwt := resolve("Bearer "+goJWT(t, testKeys()[0], f.clk.now()), tlsConn)
	sat := resolve("Bearer "+f.v.sat, tlsConn)
	pat := resolve("Bearer "+f.v.pat, tlsConn)
	anon := resolve("", plainStranger)
	user := resolve("Bearer "+idToken(testPublicURL+"/issuers/demo-project"), plainStranger)

	for name, c := range map[string]*Caller{"jwt": jwt, "sat": sat, "pat": pat, "anonymous": anon, "end user": user} {
		if err := f.d.Admit(t.Context(), c, testProject); err != nil {
			t.Errorf("%s on its project: %v", name, err)
		}
	}
	for name, c := range map[string]*Caller{"jwt": jwt, "sat": sat, "pat": pat} {
		if err := f.d.Admit(t.Context(), c, otherProject); apierr.CodeOf(err) != apierr.PermissionDenied {
			t.Errorf("%s on another project: %v, want PERMISSION_DENIED", name, err)
		}
	}
	for name, c := range map[string]*Caller{"anonymous": anon, "end user": user} {
		if err := f.d.Admit(t.Context(), c, otherProject); err != nil {
			t.Errorf("%s: the rules decide, not Admit: %v", name, err)
		}
	}
	if user.UnverifiedIDToken == "" || user.Principal != nil {
		t.Errorf("end user: %+v", user)
	}

	noOwners := newDocs(t, func(o *DocumentsOptions) { o.Owners = nil })
	c, err := noOwners.d.Resolve(t.Context(), "Bearer "+noOwners.v.pat, tlsConn)
	if err != nil {
		t.Fatal(err)
	}
	if err := noOwners.d.Admit(t.Context(), c, testProject); apierr.CodeOf(err) != apierr.PermissionDenied {
		t.Errorf("aruma_pat_ without an ownership check: %v", err)
	}
}

func TestDocumentsRecheck(t *testing.T) {
	f := newDocs(t, nil)
	k := testKeys()
	jwt, err := f.d.Resolve(t.Context(), "Bearer "+nodeJWT(t, k[0], f.clk.now()), tlsConn)
	if err != nil {
		t.Fatal(err)
	}
	pat, _ := f.d.Resolve(t.Context(), "Bearer "+f.v.pat, tlsConn)
	sat, _ := f.d.Resolve(t.Context(), "Bearer "+f.v.sat, tlsConn)
	for _, c := range []*Caller{jwt, pat, sat} {
		if err := f.d.Recheck(t.Context(), c, testProject); err != nil {
			t.Fatalf("recheck of a valid caller: %v", err)
		}
		if err := f.d.Recheck(t.Context(), c, otherProject); apierr.CodeOf(err) != apierr.PermissionDenied {
			t.Fatalf("recheck against another project: %v", err)
		}
	}

	// Deleting the key closes the stream at its next check, though the cache still holds it.
	f.dir.deleteKey(testEmail, k[0].id)
	if err := f.d.Recheck(t.Context(), jwt, testProject); apierr.CodeOf(err) != apierr.Unauthenticated {
		t.Errorf("recheck after the key's deletion: %v", err)
	}
	f.v.revoke()
	for _, c := range []*Caller{pat, sat} {
		if err := f.d.Recheck(t.Context(), c, testProject); apierr.CodeOf(err) != apierr.Unauthenticated {
			t.Errorf("recheck of a revoked token: %v", err)
		}
	}
}

// Supervise closes a service account's stream at exp, and within 60 s of the account being
// disabled (ADR-029 §3.8; ADR-026 §4).
func TestDocumentsSupervise(t *testing.T) {
	testKeys() // outside the bubble
	synctest.Test(t, func(t *testing.T) {
		f := newDocs(t, nil)
		f.d.opts.ServiceAccountJWTs.now = time.Now
		k := testKeys()
		start := time.Now()
		c, err := f.d.Resolve(t.Context(), "Bearer "+goJWT(t, k[0], start), tlsConn)
		if err != nil {
			t.Fatal(err)
		}
		err = f.d.Supervise(t.Context(), c, testProject)
		if apierr.CodeOf(err) != apierr.Unauthenticated || time.Since(start) != time.Hour {
			t.Fatalf("closed after %v with %v, want UNAUTHENTICATED at exp", time.Since(start), err)
		}

		start = time.Now()
		c, err = f.d.Resolve(t.Context(), "Bearer "+nodeJWT(t, k[1], start), tlsConn)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(30 * time.Second)
		f.dir.update(testEmail, func(r *ServiceAccountRecord) { r.Usable = false })
		err = f.d.Supervise(t.Context(), c, testProject)
		if apierr.CodeOf(err) != apierr.Unauthenticated || time.Since(start) > 30*time.Second+RecheckInterval {
			t.Fatalf("closed after %v with %v, want UNAUTHENTICATED within 60 s of disabling", time.Since(start), err)
		}

		// An anonymous stream is watched until it ends.
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
		defer cancel()
		anon, _ := f.d.Resolve(ctx, "", plainStranger)
		if err := f.d.Supervise(ctx, anon, testProject); err != nil {
			t.Fatalf("anonymous stream: %v", err)
		}
	})
}

// ADR-029 §1: only DocumentsPolicy accepts a service account JWT or the dev owner token.
func TestOnlyDocumentsPolicySetsItsFlags(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	allowed := filepath.Join(root, "internal", "platform", "authn")
	fset := token.NewFileSet()
	found := 0
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); path != root && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "third_party" || name == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, ".gen.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil // another module's file, or one being written
		}
		flag := func(n ast.Node, name string) {
			if name != "ServiceAccountJWT" && name != "DevOwner" {
				return
			}
			found++
			if filepath.Dir(path) != allowed || (!strings.HasSuffix(path, "_test.go") && filepath.Base(path) != "documents.go") {
				t.Errorf("%s: %s is set outside DocumentsPolicy", fset.Position(n.Pos()), name)
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.CompositeLit: // Policy{...} or authn.Policy{...}
				var typ string
				switch t := n.Type.(type) {
				case *ast.Ident:
					typ = t.Name
				case *ast.SelectorExpr:
					typ = t.Sel.Name
				}
				if typ != "Policy" {
					return true
				}
				for _, e := range n.Elts {
					if kv, ok := e.(*ast.KeyValueExpr); ok {
						if id, ok := kv.Key.(*ast.Ident); ok {
							flag(kv, id.Name)
						}
					}
				}
			case *ast.AssignStmt: // p.ServiceAccountJWT = true
				for _, l := range n.Lhs {
					if sel, ok := l.(*ast.SelectorExpr); ok {
						flag(sel, sel.Sel.Name)
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found == 0 {
		t.Fatal("the scan found not even DocumentsPolicy")
	}
}
