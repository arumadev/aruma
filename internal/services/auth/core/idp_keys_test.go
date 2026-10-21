package core

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arumadev/aruma/internal/services/auth/tokens"
)

func TestCacheTTL(t *testing.T) {
	for header, want := range map[string]time.Duration{
		"":                                 time.Hour,
		"public, max-age=19800, immutable": 19800 * time.Second,
		"max-age=10":                       jwksMinTTL,
		"max-age=999999":                   jwksMaxTTL,
		"max-age=bogus":                    time.Hour,
	} {
		if got := cacheTTL(header); got != want {
			t.Errorf("cacheTTL(%q) = %v, want %v", header, got, want)
		}
	}
}

func TestProviderClaimShapes(t *testing.T) {
	var c providerClaims
	if err := json.Unmarshal([]byte(`{"aud":["a","b"],"email_verified":"true"}`), &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Aud) != 2 || !c.EmailVerified {
		t.Fatalf("list audience and string boolean: %+v", c)
	}
	if err := json.Unmarshal([]byte(`{"aud":"a","email_verified":"false"}`), &c); err != nil || len(c.Aud) != 1 || c.EmailVerified {
		t.Fatalf("string audience and false: %+v %v", c, err)
	}
}

// TestJWKSCache covers rotation (an unknown kid refetches, at most once a minute), expiry, and
// an unreachable provider (known keys keep working).
func TestJWKSCache(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	small, _ := rsa.GenerateKey(rand.Reader, 1024)
	kids := atomic.Value{}
	kids.Store([]string{"k1"})
	var fetches atomic.Int32
	down := atomic.Bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		if down.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		var keys []map[string]string
		for _, kid := range kids.Load().([]string) {
			keys = append(keys, map[string]string{"kty": "RSA", "kid": kid, "alg": "RS256", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": "AQAB"})
		}
		keys = append(keys, map[string]string{"kty": "RSA", "kid": "weak", "n": base64.RawURLEncoding.EncodeToString(small.N.Bytes()), "e": "AQAB"})
		w.Header().Set("Cache-Control", "max-age=600")
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": keys})
	}))
	defer srv.Close()

	now := time.Now()
	c := newJWKSCache(srv.Client())
	c.now = func() time.Time { return now }
	ctx := context.Background()
	get := func(kid string) error { _, err := c.key(ctx, srv.URL, kid); return err }

	if err := get("k1"); err != nil || fetches.Load() != 1 {
		t.Fatalf("first fetch: %v, %d fetches", err, fetches.Load())
	}
	if err := get("k1"); err != nil || fetches.Load() != 1 {
		t.Fatalf("cached: %v, %d fetches", err, fetches.Load())
	}
	if err := get("weak"); !errors.Is(err, tokens.ErrSignature) {
		t.Fatalf("a 1024-bit key must be ignored: %v", err)
	}
	kids.Store([]string{"k1", "k2"})
	if err := get("k2"); !errors.Is(err, tokens.ErrSignature) || fetches.Load() != 1 {
		t.Fatalf("refetch within a minute: %v, %d fetches", err, fetches.Load())
	}
	now = now.Add(2 * time.Minute)
	if err := get("k2"); err != nil || fetches.Load() != 2 {
		t.Fatalf("rotation: %v, %d fetches", err, fetches.Load())
	}
	down.Store(true)
	now = now.Add(time.Hour)
	if err := get("k1"); err != nil {
		t.Fatalf("known key while the provider is down: %v", err)
	}
	if err := get("k3"); err == nil {
		t.Fatal("unknown key while the provider is down")
	}
}
