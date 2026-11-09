// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

package adminauth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"firebase.google.com/go/v4/auth"
)

// rotatingIssuer publishes whichever keys are in `keys`, and counts the key-set fetches.
type rotatingIssuer struct {
	srv    *httptest.Server
	mu     sync.Mutex
	keys   map[string]*rsa.PrivateKey
	maxAge string
	jwks   atomic.Int32
}

func newRotatingIssuer(t *testing.T) *rotatingIssuer {
	t.Helper()
	ri := &rotatingIssuer{keys: map[string]*rsa.PrivateKey{}, maxAge: "max-age=60"}
	ri.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		iss := ri.srv.URL + "/issuers/demo-app"
		w.Header().Set("Cache-Control", ri.maxAge)
		switch {
		case strings.HasSuffix(r.URL.Path, "/openid-configuration"):
			_ = json.NewEncoder(w).Encode(map[string]string{"issuer": iss, "jwks_uri": iss + "/.well-known/jwks.json"})
		case strings.HasSuffix(r.URL.Path, "/jwks.json"):
			ri.jwks.Add(1)
			ri.mu.Lock()
			defer ri.mu.Unlock()
			var ks []map[string]string
			for kid, k := range ri.keys {
				ks = append(ks, map[string]string{"kty": "RSA", "alg": "RS256", "use": "sig", "kid": kid,
					"n": base64.RawURLEncoding.EncodeToString(k.N.Bytes()),
					"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(k.E)).Bytes())})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": ks})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ri.srv.Close)
	return ri
}

func (ri *rotatingIssuer) publish(t *testing.T, kid string) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ri.mu.Lock()
	ri.keys[kid] = k
	ri.mu.Unlock()
	return k
}

func (ri *rotatingIssuer) retract(kid string) {
	ri.mu.Lock()
	delete(ri.keys, kid)
	ri.mu.Unlock()
}

func (ri *rotatingIssuer) token(kid string, key *rsa.PrivateKey, now time.Time) string {
	n := now.Unix()
	return signRS256(key, map[string]any{"alg": "RS256", "kid": kid, "typ": "JWT"},
		map[string]any{"iss": ri.srv.URL + "/issuers/demo-app", "aud": "demo-app", "sub": "user-1", "iat": n, "exp": n + 3600, "auth_time": n})
}

// A key published after the cache was loaded: a token signed by it is refused inside the refetch
// cooldown (documented trade-off), and accepted once the cooldown has passed. A key that is
// retracted stays trusted until the key set's max-age ends.
func TestVerifierKeyRotation(t *testing.T) {
	if _, err := firebaseErrorType(); err != nil {
		t.Fatal(err)
	}
	ri := newRotatingIssuer(t)
	k1 := ri.publish(t, "k1")
	clock := time.Now()
	v := newVerifier(ri.srv.URL, "demo-app", idTokenKind, http.DefaultClient)
	v.now = func() time.Time { return clock }
	ctx := context.Background()

	if _, err := v.verify(ctx, ri.token("k1", k1, clock)); err != nil {
		t.Fatalf("k1: %v", err)
	}
	k2 := ri.publish(t, "k2")
	tok2 := ri.token("k2", k2, clock)

	clock = clock.Add(10 * time.Second) // inside the cooldown: no refetch, the new key is unknown
	if _, err := v.verify(ctx, tok2); !auth.IsIDTokenInvalid(err) {
		t.Fatalf("inside the cooldown: want ID_TOKEN_INVALID, got %v", err)
	}
	if got := ri.jwks.Load(); got != 1 {
		t.Fatalf("inside the cooldown: %d key-set fetches, want 1", got)
	}
	clock = clock.Add(25 * time.Second) // 35 s after the load, the cache (60 s) is still fresh
	if _, err := v.verify(ctx, tok2); err != nil {
		t.Fatalf("after the cooldown the new key must be fetched: %v", err)
	}
	if got := ri.jwks.Load(); got != 2 {
		t.Fatalf("%d key-set fetches, want 2", got)
	}

	// The old key is withdrawn: still trusted from the cache, refused after the max-age.
	ri.retract("k1")
	if _, err := v.verify(ctx, ri.token("k1", k1, clock)); err != nil {
		t.Fatalf("a retracted key stays trusted until max-age: %v", err)
	}
	clock = clock.Add(61 * time.Second)
	if _, err := v.verify(ctx, ri.token("k1", k1, clock)); !auth.IsIDTokenInvalid(err) {
		t.Fatalf("a retracted key after max-age: want ID_TOKEN_INVALID, got %v", err)
	}
}

// Many verifications at once load the key set once, not once each.
func TestVerifierConcurrentVerifyLoadsKeysOnce(t *testing.T) {
	ri := newRotatingIssuer(t)
	k1 := ri.publish(t, "k1")
	v := newVerifier(ri.srv.URL, "demo-app", idTokenKind, http.DefaultClient)
	tok := ri.token("k1", k1, time.Now())
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := v.verify(context.Background(), tok); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := ri.jwks.Load(); got != 1 {
		t.Fatalf("%d key-set fetches for one cold cache, want 1", got)
	}
}

// A hung key-set server must not make a verification outlive its own context just because another
// verification holds the lock.
func TestVerifierHonoursContextWhileAnotherLoads(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	v := newVerifier(srv.URL, "demo-app", idTokenKind, &http.Client{Timeout: 20 * time.Second})
	go v.verify(context.Background(), "a.b.c") // never reaches the key load: not a valid token
	ri := newRotatingIssuer(t)
	k := ri.publish(t, "k1")
	_ = k
	start := time.Now()
	// First, a verification with a background context that hangs in load...
	tok := signRS256(k, map[string]any{"alg": "RS256", "kid": "k1"}, map[string]any{"iss": srv.URL + "/issuers/demo-app", "aud": "demo-app", "sub": "u", "iat": time.Now().Unix(), "exp": time.Now().Unix() + 60, "auth_time": time.Now().Unix()})
	go v.verify(context.Background(), tok)
	time.Sleep(200 * time.Millisecond)
	// ...then one that has 300 ms to live.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := v.verify(ctx, tok); done <- err }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("verify ignored its context for %v while another verification held the key-set lock", time.Since(start))
	}
}
