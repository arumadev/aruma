// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

package adminauth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"firebase.google.com/go/v4/auth"
)

// Verification throughput against the live server TestMain starts, for the Phase 2
// performance baseline:
//
//	go test -run '^$' -bench Verify -benchtime 5s -cpu 1,4,8
//
// The keys come from the server's discovery document and JWKS over loopback, then the cache
// serves them: http-requests/op shows how rarely the library goes back to the server.

// countingTransport counts the requests the library makes, then sends them with next.
type countingTransport struct {
	n    atomic.Int64
	next http.RoundTripper
}

func (c *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.n.Add(1)
	return c.next.RoundTrip(r)
}

var benchTokens struct {
	once sync.Once
	toks []string
	err  error
}

// liveIDTokens returns ID tokens of 8 users, signed up once for all benchmarks: sign-up is
// limited per address, and each benchmark function runs several times.
func liveIDTokens(b *testing.B) []string {
	b.Helper()
	benchTokens.once.Do(func() {
		for i := range 8 {
			body, _ := json.Marshal(map[string]any{"email": fmt.Sprintf("bench-verify-%d@example.com", i), "password": "secret1", "returnSecureToken": true})
			resp, err := http.Post(live.url+"/identitytoolkit.googleapis.com/v1/accounts:signUp?key="+live.expected.Web.APIKey, "application/json", bytes.NewReader(body))
			if err != nil {
				benchTokens.err = err
				return
			}
			var out struct {
				IDToken string `json:"idToken"`
			}
			err = json.NewDecoder(resp.Body).Decode(&out)
			resp.Body.Close()
			if err != nil || out.IDToken == "" {
				benchTokens.err = fmt.Errorf("sign up: HTTP %d %v", resp.StatusCode, err)
				return
			}
			benchTokens.toks = append(benchTokens.toks, out.IDToken)
		}
	})
	if benchTokens.err != nil {
		b.Fatal(benchTokens.err)
	}
	return benchTokens.toks
}

// benchVerify runs verify from GOMAXPROCS goroutines with a client whose requests go through
// next, after one untimed call that fetches the keys (and the access token).
func benchVerify(b *testing.B, next http.RoundTripper, verify func(*Client, context.Context, string) (*auth.Token, error)) {
	toks := liveIDTokens(b)
	ct := &countingTransport{next: next}
	c, err := NewClient(context.Background(), nil,
		WithCredentialsFile(filepath.Join(live.dir, "service-account.json")), WithHTTPClient(&http.Client{Transport: ct}))
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	if _, err := verify(c, ctx, toks[0]); err != nil {
		b.Fatal(err)
	}
	ct.n.Store(0)
	var i atomic.Uint64
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := verify(c, ctx, toks[i.Add(1)%uint64(len(toks))]); err != nil {
				b.Error(err)
				return
			}
		}
	})
	b.ReportMetric(float64(ct.n.Load())/float64(b.N), "http-requests/op")
}

// BenchmarkVerifyIDToken is local verification: claims, then the RS256 signature against the
// cached JWKS.
func BenchmarkVerifyIDToken(b *testing.B) {
	benchVerify(b, http.DefaultTransport, (*Client).VerifyIDToken)
}

// BenchmarkVerifyIDTokenAndCheckRevoked adds the revocation check, one Admin API lookup per
// verification. With the library's default HTTP client (http.DefaultTransport keeps 2 idle
// connections per host) and with a transport that keeps enough for every goroutine.
func BenchmarkVerifyIDTokenAndCheckRevoked(b *testing.B) {
	b.Run("default-transport", func(b *testing.B) {
		benchVerify(b, http.DefaultTransport, (*Client).VerifyIDTokenAndCheckRevoked)
	})
	b.Run("pooled-transport", func(b *testing.B) {
		t := http.DefaultTransport.(*http.Transport).Clone() // keeps TestMain's loopback guard
		t.MaxIdleConnsPerHost = 64
		benchVerify(b, t, (*Client).VerifyIDTokenAndCheckRevoked)
	})
}
