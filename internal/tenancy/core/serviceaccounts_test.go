package core

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/arumadev/aruma/internal/platform/apierr"
	"github.com/arumadev/aruma/internal/platform/authn"
	acrypto "github.com/arumadev/aruma/internal/platform/crypto"
	"github.com/arumadev/aruma/internal/store/storetest"
)

// Service account credentials on Postgres, as the Document Database sees them (ADR-029): the
// scopes an aruma_sat_ token carries, and the revocations its caches hear of.

const saTokenURL = "http://127.0.0.1/oauth2/v1/token"

// revocationLog records what OnServiceAccountRevoked tells.
type revocationLog struct {
	mu       sync.Mutex
	keys     []string
	accounts []uuid.UUID
	projects []int64
}

func (r *revocationLog) ForgetKey(keyID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keys = append(r.keys, keyID)
}

func (r *revocationLog) ForgetServiceAccount(id uuid.UUID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.accounts = append(r.accounts, id)
}

func (r *revocationLog) ForgetProject(project int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.projects = append(r.projects, project)
}

func (r *revocationLog) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.keys) + len(r.accounts) + len(r.projects)
}

// saService is a Service on a fresh database, with token keys, an owner, a project, and a
// service account with one key. A missing Postgres fails the test rather than skipping it.
func saService(t *testing.T) (*Service, *authn.Principal, Project, ServiceAccount, GeneratedKey) {
	t.Helper()
	t.Setenv("ARUMA_TEST_REQUIRE_DB", "1")
	pool := storetest.NewPool(t)
	s := rulesService(t, pool, rulesDomain, nil)
	master := make([]byte, 32)
	if _, err := rand.Read(master); err != nil {
		t.Fatal(err)
	}
	keys, err := acrypto.NewKeys(master)
	if err != nil {
		t.Fatal(err)
	}
	s.keys = keys
	owner, proj := rulesOwner(t, s, pool, "owner@example.com", "chat-app")
	sa, err := s.CreateServiceAccount(t.Context(), owner, proj.ID, "firestore-admin", "Firestore admin")
	if err != nil {
		t.Fatal(err)
	}
	key, err := s.CreateServiceAccountKey(t.Context(), owner, proj.ID, sa.ID)
	if err != nil {
		t.Fatal(err)
	}
	return s, owner, proj, sa, key
}

// signSAJWT signs claims with the key file's private key, kid its private_key_id.
func signSAJWT(t *testing.T, key GeneratedKey, claims map[string]any) string {
	t.Helper()
	block, _ := pem.Decode([]byte(key.PrivateKeyPEM))
	if block == nil {
		t.Fatal("no PEM private key")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	seg := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(b)
	}
	input := seg(map[string]any{"alg": "RS256", "typ": "JWT", "kid": key.Key.KeyID}) + "." + seg(claims)
	d := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, k.(*rsa.PrivateKey), crypto.SHA256, d[:])
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// An aruma_sat_ principal carries the scopes its assertion asked for (ADR-029 §2: the scopes
// registered with the token are held to §3.4), and none when it asked for none.
func TestServiceTokenCarriesScopes(t *testing.T) {
	s, _, proj, sa, key := saService(t)
	now := time.Now().Unix()
	for _, c := range []struct {
		scope any // nil: no scope claim
		want  []string
	}{
		{authn.ScopeCloudPlatform, []string{authn.ScopeCloudPlatform}},
		{authn.ScopeCloudPlatform + " " + authn.ScopeDatastore, []string{authn.ScopeCloudPlatform, authn.ScopeDatastore}},
		{"https://www.googleapis.com/auth/devstorage.read_only", []string{"https://www.googleapis.com/auth/devstorage.read_only"}},
		{nil, []string{}},
	} {
		claims := map[string]any{"iss": sa.Email, "aud": saTokenURL, "iat": now - 10, "exp": now + 3590}
		if c.scope != nil {
			claims["scope"] = c.scope
		}
		tok, err := s.ExchangeAssertion(t.Context(), signSAJWT(t, key, claims), saTokenURL)
		if err != nil {
			t.Fatalf("scope %v: %v", c.scope, err)
		}
		p, err := s.VerifyServiceToken(t.Context(), tok.AccessToken)
		if err != nil {
			t.Fatalf("scope %v: %v", c.scope, err)
		}
		if p.Kind != authn.KindServiceAccount || p.ServiceAccountID != sa.ID || p.ProjectNumber != proj.Number {
			t.Errorf("scope %v: principal %+v", c.scope, p)
		}
		if p.Scopes == nil || !slices.Equal(p.Scopes, c.want) {
			t.Errorf("scope %v: Scopes = %#v, want %#v", c.scope, p.Scopes, c.want)
		}
	}
}

// Deleting a key, an account or a project tells every registered listener, once committed; a
// deletion that fails tells no one.
func TestServiceAccountRevocationHook(t *testing.T) {
	s, owner, proj, sa, key := saService(t)
	var a, b revocationLog
	s.OnServiceAccountRevoked(&a)
	s.OnServiceAccountRevoked(&b)

	// Failures first: an unknown key, an unknown account, another owner's project.
	if err := s.DeleteServiceAccountKey(t.Context(), owner, proj.ID, sa.ID, "no-such-key"); apierr.CodeOf(err) != apierr.NotFound {
		t.Fatalf("deleting an unknown key: %v", err)
	}
	if err := s.DeleteServiceAccount(t.Context(), owner, proj.ID, uuid.New()); err == nil {
		t.Fatal("deleting an unknown account succeeded")
	}
	stranger := &authn.Principal{Kind: authn.KindAccount, AccountID: uuid.New()}
	if _, err := s.DeleteProject(t.Context(), stranger, proj.ID); err == nil {
		t.Fatal("a stranger deleted the project")
	}
	if n := a.count(); n != 0 {
		t.Fatalf("failed deletions told the listener %d times", n)
	}

	if err := s.DeleteServiceAccountKey(t.Context(), owner, proj.ID, sa.ID, key.Key.KeyID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteServiceAccount(t.Context(), owner, proj.ID, sa.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteProject(t.Context(), owner, proj.ID); err != nil {
		t.Fatal(err)
	}
	for _, r := range []*revocationLog{&a, &b} {
		if !slices.Equal(r.keys, []string{key.Key.KeyID}) || !slices.Equal(r.accounts, []uuid.UUID{sa.ID}) ||
			!slices.Equal(r.projects, []int64{proj.Number}) {
			t.Errorf("listener heard keys %v, accounts %v, projects %v", r.keys, r.accounts, r.projects)
		}
	}
}

// End to end with the Document Database's verifier on this Service as its directory: a token
// it cached is refused as soon as its key's deletion returns, not VerifiedTokenTTL later.
func TestKeyDeletionRevokesCachedJWT(t *testing.T) {
	s, owner, proj, sa, key := saService(t)
	v := authn.NewServiceAccountJWTVerifier(s, s.opts.PublicURL, nil)
	s.OnServiceAccountRevoked(v)

	second, err := s.CreateServiceAccountKey(t.Context(), owner, proj.ID, sa.ID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	jwt := func(k GeneratedKey) string {
		return signSAJWT(t, k, map[string]any{"iss": sa.Email, "sub": sa.Email, "aud": "", "scope": authn.ScopeDatastore, "iat": now, "exp": now + 3600})
	}
	first, other := jwt(key), jwt(second)
	for _, tok := range []string{first, other} {
		p, err := v.Verify(t.Context(), tok)
		if err != nil {
			t.Fatal(err)
		}
		if p.ServiceAccountID != sa.ID || p.ProjectNumber != proj.Number {
			t.Fatalf("principal %+v", p)
		}
	}

	if err := s.DeleteServiceAccountKey(t.Context(), owner, proj.ID, sa.ID, key.Key.KeyID); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(t.Context(), first); apierr.CodeOf(err) != apierr.Unauthenticated {
		t.Fatalf("a JWT of the deleted key: %v, want UNAUTHENTICATED", err)
	}
	if _, err := v.Verify(t.Context(), other); err != nil {
		t.Fatalf("a JWT of the remaining key: %v", err)
	}

	if err := s.DeleteServiceAccount(t.Context(), owner, proj.ID, sa.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(t.Context(), other); apierr.CodeOf(err) != apierr.Unauthenticated {
		t.Fatalf("a JWT of the deleted account: %v, want UNAUTHENTICATED", err)
	}
}
