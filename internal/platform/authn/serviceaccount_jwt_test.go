package authn

import (
	"bytes"
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"log/slog"
	"math/big"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/arumadev/aruma/internal/platform/apierr"
)

// Every key here is made in process; no Google key is ever used.

type testKey struct {
	id   string
	priv *rsa.PrivateKey
	cert string // PEM
}

var testKeys = sync.OnceValue(func() []testKey {
	keys := make([]testKey, 3)
	for i := range keys {
		priv, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(int64(i + 1)),
			Subject:      pkix.Name{CommonName: "test"},
			NotBefore:    time.Unix(0, 0),
			NotAfter:     time.Unix(1<<35, 0),
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
		if err != nil {
			panic(err)
		}
		keys[i] = testKey{
			id:   strings.Repeat(string(rune('a'+i)), 40),
			priv: priv,
			cert: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		}
	}
	return keys
})

const (
	testEmail     = "firestore-admin@demo-project.sa.aruma.test"
	otherEmail    = "other@other-project.sa.aruma.test"
	testPublicURL = "https://aruma.example.com"
	testProject   = int64(1001)
	otherProject  = int64(2002)
	googleScopes  = ScopeCloudPlatform + " " + ScopeDatastore
)

// fakeDirectory is a mutable ServiceAccountDirectory.
type fakeDirectory struct {
	mu       sync.Mutex
	accounts map[string]ServiceAccountRecord
	err      error
	lookups  int
}

func newFakeDirectory() *fakeDirectory {
	k := testKeys()
	return &fakeDirectory{accounts: map[string]ServiceAccountRecord{
		testEmail: {ID: uuid.New(), Email: testEmail, ProjectNumber: testProject, Usable: true,
			Keys: []ServiceAccountPublicKey{{KeyID: k[0].id, CertificatePEM: k[0].cert}, {KeyID: k[1].id, CertificatePEM: k[1].cert}}},
		otherEmail: {ID: uuid.New(), Email: otherEmail, ProjectNumber: otherProject, Usable: true,
			Keys: []ServiceAccountPublicKey{{KeyID: k[2].id, CertificatePEM: k[2].cert}}},
	}}
}

func (f *fakeDirectory) LookupServiceAccount(_ context.Context, email string) (ServiceAccountRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lookups++
	if f.err != nil {
		return ServiceAccountRecord{}, f.err
	}
	return f.accounts[email], nil
}

func (f *fakeDirectory) update(email string, change func(*ServiceAccountRecord)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.accounts[email]
	change(&r)
	f.accounts[email] = r
}

func (f *fakeDirectory) deleteKey(email, keyID string) {
	f.update(email, func(r *ServiceAccountRecord) {
		r.Keys = slices.DeleteFunc(slices.Clone(r.Keys), func(k ServiceAccountPublicKey) bool { return k.KeyID == keyID })
	})
}

func (f *fakeDirectory) id(email string) uuid.UUID {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.accounts[email].ID
}

func seg(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// signJWT signs header.claims with k by header's alg: RS256, RS384, or HS256 with the key's
// public certificate as the HMAC secret.
func signJWT(t *testing.T, k testKey, header, claims map[string]any) string {
	t.Helper()
	input := seg(header) + "." + seg(claims)
	var sig []byte
	var err error
	switch header["alg"] {
	case "RS256":
		d := sha256.Sum256([]byte(input))
		sig, err = rsa.SignPKCS1v15(rand.Reader, k.priv, crypto.SHA256, d[:])
	case "RS384":
		d := sha512.Sum384([]byte(input))
		sig, err = rsa.SignPKCS1v15(rand.Reader, k.priv, crypto.SHA384, d[:])
	case "HS256":
		m := hmac.New(sha256.New, []byte(k.cert))
		m.Write([]byte(input))
		sig = m.Sum(nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// goJWT is the shape cloud.google.com/go/firestore 1.29.0 sends (spike, Q3): kid, aud "".
func goJWT(t *testing.T, k testKey, now time.Time) string {
	iat := now.Unix()
	return signJWT(t, k,
		map[string]any{"alg": "RS256", "kid": k.id, "typ": "JWT"},
		map[string]any{"iss": testEmail, "sub": testEmail, "scope": googleScopes, "aud": "", "iat": iat, "exp": iat + 3600})
}

// nodeJWT is the shape firebase-admin 14.5.0 sends (spike, Q3): no kid, no aud.
func nodeJWT(t *testing.T, k testKey, now time.Time) string {
	iat := now.Unix()
	return signJWT(t, k,
		map[string]any{"alg": "RS256", "typ": "JWT"},
		map[string]any{"iss": testEmail, "sub": testEmail, "scope": googleScopes, "iat": iat, "exp": iat + 3600})
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newTestVerifier(t *testing.T) (*ServiceAccountJWTVerifier, *fakeDirectory, *clock, *bytes.Buffer) {
	t.Helper()
	dir := newFakeDirectory()
	var logs bytes.Buffer
	v := NewServiceAccountJWTVerifier(dir, testPublicURL+"/", slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	v.now = c.now
	return v, dir, c, &logs
}

func TestServiceAccountJWTSpikeShapes(t *testing.T) {
	v, dir, c, _ := newTestVerifier(t)
	k := testKeys()
	for name, tok := range map[string]string{
		"go":                         goJWT(t, k[0], c.now()),
		"node":                       nodeJWT(t, k[0], c.now()),
		"node, signed by second key": nodeJWT(t, k[1], c.now()),
	} {
		p, err := v.Verify(t.Context(), tok)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if p.Kind != KindServiceAccount || p.Method != MethodServiceAccountJWT || p.Email != testEmail ||
			p.ProjectNumber != testProject || p.ServiceAccountID != dir.id(testEmail) ||
			!p.ExpiresAt.Equal(c.now().Add(time.Hour)) || len(p.Scopes) != 2 {
			t.Errorf("%s: principal %+v", name, p)
		}
	}
	p, _ := v.Verify(t.Context(), nodeJWT(t, k[1], c.now()))
	if p.KeyID != k[1].id {
		t.Errorf("without kid, the verifying key is the second one: %q", p.KeyID)
	}
}

func TestServiceAccountJWTRefusals(t *testing.T) {
	v, _, c, logs := newTestVerifier(t)
	k := testKeys()
	now := c.now().Unix()
	rs := func(kid string) map[string]any {
		h := map[string]any{"alg": "RS256", "typ": "JWT"}
		if kid != "" {
			h["kid"] = kid
		}
		return h
	}
	claims := func(change func(map[string]any)) map[string]any {
		m := map[string]any{"iss": testEmail, "sub": testEmail, "scope": googleScopes, "iat": now, "exp": now + 3600}
		if change != nil {
			change(m)
		}
		return m
	}
	tamper := func(tok string) string {
		i := strings.LastIndexByte(tok, '.')
		sig, _ := base64.RawURLEncoding.DecodeString(tok[i+1:])
		sig[0] ^= 1
		return tok[:i+1] + base64.RawURLEncoding.EncodeToString(sig)
	}
	none := seg(map[string]any{"alg": "none", "typ": "JWT"}) + "." + seg(claims(nil)) + "."

	cases := map[string]string{
		"alg none":                  none,
		"HS256 with the public key": signJWT(t, k[0], map[string]any{"alg": "HS256", "typ": "JWT"}, claims(nil)),
		"RS384":                     signJWT(t, k[0], map[string]any{"alg": "RS384", "typ": "JWT"}, claims(nil)),
		"typ not JWT":               signJWT(t, k[0], map[string]any{"alg": "RS256", "typ": "at+jwt"}, claims(nil)),
		"tampered":                  tamper(signJWT(t, k[0], rs(""), claims(nil))),
		"unknown kid":               signJWT(t, k[0], rs("nope"), claims(nil)),
		"kid of the other key, signed by the first": signJWT(t, k[0], rs(k[1].id), claims(nil)),
		"kid of another account's key":              signJWT(t, k[2], rs(k[2].id), claims(nil)),
		"iss != sub":                                signJWT(t, k[0], rs(k[0].id), claims(func(m map[string]any) { m["sub"] = "someone@else" })),
		"no sub":                                    signJWT(t, k[0], rs(k[0].id), claims(func(m map[string]any) { delete(m, "sub") })),
		"empty iss":                                 signJWT(t, k[0], rs(k[0].id), claims(func(m map[string]any) { m["iss"], m["sub"] = "", "" })),
		"unknown email": signJWT(t, k[0], rs(""), claims(func(m map[string]any) {
			m["iss"], m["sub"] = "ghost@demo-project.sa.aruma.test", "ghost@demo-project.sa.aruma.test"
		})),
		"another account's email, our key": signJWT(t, k[0], rs(""), claims(func(m map[string]any) { m["iss"], m["sub"] = otherEmail, otherEmail })),
		"no scope":                         signJWT(t, k[0], rs(""), claims(func(m map[string]any) { delete(m, "scope") })),
		"no scope, Firestore audience":     signJWT(t, k[0], rs(""), claims(func(m map[string]any) { delete(m, "scope"); m["aud"] = FirestoreAudience })),
		"scope without datastore":          signJWT(t, k[0], rs(""), claims(func(m map[string]any) { m["scope"] = "https://www.googleapis.com/auth/devstorage.full_control" })),
		"aud is the token endpoint":        signJWT(t, k[0], rs(""), claims(func(m map[string]any) { m["aud"] = testPublicURL + "/oauth2/v1/token" })),
		"aud another service":              signJWT(t, k[0], rs(""), claims(func(m map[string]any) { m["aud"] = "https://storage.googleapis.com/" })),
		"aud array with a stranger":        signJWT(t, k[0], rs(""), claims(func(m map[string]any) { m["aud"] = []string{"", "https://evil.example"} })),
		"aud a number":                     signJWT(t, k[0], rs(""), claims(func(m map[string]any) { m["aud"] = 7 })),
		"iat 61 s ahead":                   signJWT(t, k[0], rs(""), claims(func(m map[string]any) { m["iat"], m["exp"] = now+61, now+3000 })),
		"expired 61 s ago":                 signJWT(t, k[0], rs(""), claims(func(m map[string]any) { m["iat"], m["exp"] = now-3661, now-61 })),
		"lifetime above 1 h 60 s":          signJWT(t, k[0], rs(""), claims(func(m map[string]any) { m["exp"] = now + 3661 })),
		"exp before iat":                   signJWT(t, k[0], rs(""), claims(func(m map[string]any) { m["exp"] = now - 1 })),
		"no iat":                           signJWT(t, k[0], rs(""), claims(func(m map[string]any) { delete(m, "iat") })),
		"huge exp":                         signJWT(t, k[0], rs(""), claims(func(m map[string]any) { m["exp"] = 1e300 })),
		"two segments":                     "a.b",
	}
	for name, tok := range cases {
		_, err := v.Verify(t.Context(), tok)
		if apierr.CodeOf(err) != apierr.Unauthenticated {
			t.Errorf("%s: %v, want UNAUTHENTICATED", name, err)
		}
	}
	if !strings.Contains(logs.String(), "unsupported alg") || !strings.Contains(logs.String(), "alg=none") {
		t.Errorf("alg none not logged:\n%s", logs)
	}
	if strings.Contains(logs.String(), none) {
		t.Error("a token reached the log")
	}
}

func TestServiceAccountJWTAccepts(t *testing.T) {
	v, _, c, _ := newTestVerifier(t)
	k := testKeys()
	now := c.now().Unix()
	for name, change := range map[string]func(map[string]any){
		"datastore scope only":           func(m map[string]any) { m["scope"] = ScopeDatastore },
		"cloud-platform among others":    func(m map[string]any) { m["scope"] = "openid " + ScopeCloudPlatform },
		"Firestore audience":             func(m map[string]any) { m["aud"] = FirestoreAudience },
		"public_url audience":            func(m map[string]any) { m["aud"] = testPublicURL },
		"public_url audience with slash": func(m map[string]any) { m["aud"] = testPublicURL + "/" },
		"audience array":                 func(m map[string]any) { m["aud"] = []string{"", FirestoreAudience} },
		"null audience":                  func(m map[string]any) { m["aud"] = nil },
		"iat 60 s ahead":                 func(m map[string]any) { m["iat"], m["exp"] = now+60, now+3000 },
		"expired 59 s ago":               func(m map[string]any) { m["iat"], m["exp"] = now-3659, now-59 },
		"lifetime 1 h 60 s":              func(m map[string]any) { m["exp"] = now + 3660 },
		"no typ":                         nil,
	} {
		m := map[string]any{"iss": testEmail, "sub": testEmail, "scope": googleScopes, "iat": now, "exp": now + 3600}
		h := map[string]any{"alg": "RS256", "typ": "JWT"}
		if change != nil {
			change(m)
		} else {
			delete(h, "typ")
		}
		if _, err := v.Verify(t.Context(), signJWT(t, k[0], h, m)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestServiceAccountJWTDisabledAccount(t *testing.T) {
	v, dir, c, _ := newTestVerifier(t)
	dir.update(testEmail, func(r *ServiceAccountRecord) { r.Usable = false })
	if _, err := v.Verify(t.Context(), goJWT(t, testKeys()[0], c.now())); apierr.CodeOf(err) != apierr.Unauthenticated {
		t.Fatalf("disabled account: %v", err)
	}
}

func TestServiceAccountJWTLookupFailure(t *testing.T) {
	v, dir, c, _ := newTestVerifier(t)
	dir.err = errors.New("connection refused")
	if _, err := v.Verify(t.Context(), goJWT(t, testKeys()[0], c.now())); apierr.CodeOf(err) != apierr.Internal {
		t.Fatalf("an outage must not read as a bad token: %v", err)
	}
}

// ADR-029 §3.8: a verified token is cached for at most 60 s, so a deleted key stops working
// within a minute without a hook, and at once with one.
func TestServiceAccountJWTCache(t *testing.T) {
	v, dir, c, _ := newTestVerifier(t)
	k := testKeys()
	tok := goJWT(t, k[0], c.now())
	if _, err := v.Verify(t.Context(), tok); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(t.Context(), tok); err != nil || dir.lookups != 1 {
		t.Fatalf("second call not served from the cache: %v, %d lookups", err, dir.lookups)
	}

	dir.deleteKey(testEmail, k[0].id)
	c.add(59 * time.Second)
	if _, err := v.Verify(t.Context(), tok); err != nil {
		t.Fatalf("within 60 s the cached token stands: %v", err)
	}
	if _, err := v.Recheck(t.Context(), tok); apierr.CodeOf(err) != apierr.Unauthenticated {
		t.Fatalf("Recheck must bypass the cache: %v", err)
	}
	c.add(2 * time.Second)
	if _, err := v.Verify(t.Context(), tok); apierr.CodeOf(err) != apierr.Unauthenticated {
		t.Fatalf("60 s after the key's deletion the token still works: %v", err)
	}

	// With the hook, at once.
	tok = nodeJWT(t, k[1], c.now())
	if _, err := v.Verify(t.Context(), tok); err != nil {
		t.Fatal(err)
	}
	dir.deleteKey(testEmail, k[1].id)
	v.ForgetKey(k[1].id)
	if _, err := v.Verify(t.Context(), tok); apierr.CodeOf(err) != apierr.Unauthenticated {
		t.Fatalf("ForgetKey left the token cached: %v", err)
	}
}

func TestServiceAccountJWTCacheForgetsAccount(t *testing.T) {
	v, dir, c, _ := newTestVerifier(t)
	tok := goJWT(t, testKeys()[0], c.now())
	if _, err := v.Verify(t.Context(), tok); err != nil {
		t.Fatal(err)
	}
	dir.update(testEmail, func(r *ServiceAccountRecord) { r.Usable = false })
	v.ForgetServiceAccount(dir.id(testEmail))
	if _, err := v.Verify(t.Context(), tok); apierr.CodeOf(err) != apierr.Unauthenticated {
		t.Fatalf("ForgetServiceAccount left the token cached: %v", err)
	}
}

// The cache never outlives exp, though the skew still admits the token until exp + 60 s.
func TestServiceAccountJWTCacheStopsAtExp(t *testing.T) {
	v, dir, c, _ := newTestVerifier(t)
	k := testKeys()
	iat := c.now().Add(-time.Hour + 10*time.Second).Unix()
	tok := signJWT(t, k[0], map[string]any{"alg": "RS256", "kid": k[0].id},
		map[string]any{"iss": testEmail, "sub": testEmail, "scope": ScopeDatastore, "iat": iat, "exp": iat + 3600})
	if _, err := v.Verify(t.Context(), tok); err != nil {
		t.Fatal(err)
	}
	c.add(20 * time.Second)
	if _, err := v.Verify(t.Context(), tok); err != nil || dir.lookups != 2 {
		t.Fatalf("past exp the token must be verified again: %v, %d lookups", err, dir.lookups)
	}
}

func TestServiceAccountJWTConcurrent(t *testing.T) {
	v, _, c, _ := newTestVerifier(t)
	k := testKeys()
	toks := []string{goJWT(t, k[0], c.now()), nodeJWT(t, k[1], c.now())}
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Go(func() {
			for j := range 20 {
				if _, err := v.Verify(context.Background(), toks[(i+j)%2]); err != nil {
					t.Error(err)
					return
				}
				if j == 10 && i%8 == 0 {
					v.ForgetKey(k[0].id)
					c.add(time.Millisecond)
				}
			}
		})
	}
	wg.Wait()
}
