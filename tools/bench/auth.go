package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The Auth scenarios call the client endpoints the Firebase SDKs call (ADR-014), with the API
// key of the first project's web app, against users created before the measured phase.

const (
	itkPath = "/identitytoolkit.googleapis.com/v1/accounts:"
	stsPath = "/securetoken.googleapis.com/v1/token"
	// customTokenAud is the audience the official createCustomToken writes.
	customTokenAud = "https://identitytoolkit.googleapis.com/google.identity.identitytoolkit.v1.IdentityToolkit"
	// customTokenUsers is how many custom token users exist, one pre-signed token each.
	customTokenUsers = 64
)

// AuthFixture is the Auth data of the first project.
type AuthFixture struct {
	Base, Key string // server base URL, and the web app's API key
	Run       string // random per run, so new emails never collide with earlier runs'
	Password  string // every seeded user's password
	// One entry per seeded email/password user, from its sign-up.
	Emails, IDTokens, RefreshTokens []string
	// CustomTokens are pre-signed as createCustomToken signs them, one per user that already
	// exists, so the measured sign-ins take the returning-user path. Empty without a service
	// account key.
	CustomTokens []string
}

func (a *AuthFixture) url(path string) string { return a.Base + path + "?key=" + a.Key }

var signups atomic.Uint64

// authScenarios are the Phase 2 Auth scenarios, in the order they run. All of them spread
// their requests over client addresses: every client call counts against a per-address limit
// (300/min, burst 100), and account creation against a tighter one (2/min, burst 100).
var authScenarios = []Scenario{
	{
		Name:          "auth-signup",
		What:          "POST accounts:signUp (email/password): argon2id hash, user + provider + password + refresh token + event insert, ID token signed",
		Concurrency:   4,
		Argon2:        true,
		SpreadClients: true,
		NeedAuth:      true,
		New: func(_ *Target, f *Fixture) func(context.Context, int) (*http.Request, error) {
			a := f.Auth
			u := a.url(itkPath + "signUp")
			return func(ctx context.Context, _ int) (*http.Request, error) {
				email := fmt.Sprintf("new-%s-%d@bench.example", a.Run, signups.Add(1))
				body, _ := json.Marshal(map[string]any{"email": email, "password": a.Password, "returnSecureToken": true})
				return postJSON(ctx, u, string(body))
			}
		},
	},
	{
		Name:          "auth-signin-password",
		What:          "POST accounts:signInWithPassword: argon2id verify, refresh token insert, audit row, ID token signed",
		Concurrency:   4,
		Argon2:        true,
		SpreadClients: true,
		NeedAuth:      true,
		New: func(_ *Target, f *Fixture) func(context.Context, int) (*http.Request, error) {
			a := f.Auth
			u := a.url(itkPath + "signInWithPassword")
			bodies := make([]string, len(a.Emails))
			for i, e := range a.Emails {
				b, _ := json.Marshal(map[string]any{"email": e, "password": a.Password, "returnSecureToken": true})
				bodies[i] = string(b)
			}
			return func(ctx context.Context, _ int) (*http.Request, error) { return postJSON(ctx, u, pick(bodies)) }
		},
	},
	{
		Name:          "auth-refresh",
		What:          "POST securetoken /v1/token (refresh_token grant, form as the web SDK sends it): token and user lookup, ID token signed, two touches",
		SpreadClients: true,
		NeedAuth:      true,
		New: func(_ *Target, f *Fixture) func(context.Context, int) (*http.Request, error) {
			a := f.Auth
			u := a.url(stsPath)
			bodies := make([]string, len(a.RefreshTokens))
			for i, rt := range a.RefreshTokens {
				bodies[i] = url.Values{"grant_type": {"refresh_token"}, "refresh_token": {rt}}.Encode()
			}
			return func(ctx context.Context, _ int) (*http.Request, error) {
				req, err := http.NewRequestWithContext(ctx, "POST", u, strings.NewReader(pick(bodies)))
				if err == nil {
					req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				}
				return req, err
			}
		},
	},
	{
		Name:          "auth-lookup",
		What:          "POST accounts:lookup: ID token verified on the server (key lookup, RS256 check), user, providers and password rows",
		SpreadClients: true,
		NeedAuth:      true,
		New: func(_ *Target, f *Fixture) func(context.Context, int) (*http.Request, error) {
			a := f.Auth
			u := a.url(itkPath + "lookup")
			bodies := make([]string, len(a.IDTokens))
			for i, t := range a.IDTokens {
				b, _ := json.Marshal(map[string]string{"idToken": t})
				bodies[i] = string(b)
			}
			return func(ctx context.Context, _ int) (*http.Request, error) { return postJSON(ctx, u, pick(bodies)) }
		},
	},
	{
		Name:          "auth-signin-custom-token",
		What:          "POST accounts:signInWithCustomToken: service account key lookup, RS256 check, user + refresh token rows, ID token signed",
		SpreadClients: true,
		NeedAuth:      true,
		NeedSA:        true,
		New: func(_ *Target, f *Fixture) func(context.Context, int) (*http.Request, error) {
			a := f.Auth
			u := a.url(itkPath + "signInWithCustomToken")
			bodies := make([]string, len(a.CustomTokens))
			for i, t := range a.CustomTokens {
				b, _ := json.Marshal(map[string]any{"token": t, "returnSecureToken": true})
				bodies[i] = string(b)
			}
			return func(ctx context.Context, _ int) (*http.Request, error) { return postJSON(ctx, u, pick(bodies)) }
		},
	},
}

func postJSON(ctx context.Context, u, body string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", u, strings.NewReader(body))
	if err == nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, err
}

// seedAuth creates the Auth fixture in project: users email/password users signed up through
// the client API, as an app would, and, with a service account key, the custom token users.
// Sign-ups run four at a time, the server's argon2id gate.
func seedAuth(ctx context.Context, hc *http.Client, c *apiClient, base, project, apiKeyID, run string, users int, sa *serviceAccountKey) (*AuthFixture, error) {
	var key struct {
		KeyString string `json:"keyString"`
	}
	if err := c.json(ctx, "GET", "/projects/"+project+"/apiKeys/"+apiKeyID, nil, &key); err != nil {
		return nil, fmt.Errorf("read the web app's API key: %w", err)
	}
	a := &AuthFixture{Base: base, Key: key.KeyString, Run: run, Password: "bench-" + randomHex(8),
		Emails: make([]string, users), IDTokens: make([]string, users), RefreshTokens: make([]string, users)}

	err := parallel(ctx, users, 4, func(i int) error {
		a.Emails[i] = fmt.Sprintf("user-%s-%04d@bench.example", run, i)
		var out struct {
			IDToken      string `json:"idToken"`
			RefreshToken string `json:"refreshToken"`
		}
		if err := authCall(ctx, hc, a.url(itkPath+"signUp"), map[string]any{"email": a.Emails[i], "password": a.Password, "returnSecureToken": true}, &out); err != nil {
			return fmt.Errorf("sign up %s: %w", a.Emails[i], err)
		}
		a.IDTokens[i], a.RefreshTokens[i] = out.IDToken, out.RefreshToken
		return nil
	})
	if err != nil {
		return nil, err
	}

	if sa == nil {
		return a, nil
	}
	now := time.Now().Unix()
	a.CustomTokens = make([]string, customTokenUsers)
	for i := range a.CustomTokens {
		tok, err := sa.sign(map[string]any{
			"iss": sa.Email, "sub": sa.Email, "aud": customTokenAud, "iat": now, "exp": now + 3600,
			"uid": fmt.Sprintf("ct-%s-%03d", run, i), "claims": map[string]any{"role": "bench"},
		})
		if err != nil {
			return nil, err
		}
		a.CustomTokens[i] = tok
	}
	// The first sign-in creates the user; the measured ones find it.
	err = parallel(ctx, len(a.CustomTokens), 8, func(i int) error {
		if err := authCall(ctx, hc, a.url(itkPath+"signInWithCustomToken"), map[string]any{"token": a.CustomTokens[i], "returnSecureToken": true}, nil); err != nil {
			return fmt.Errorf("custom token sign-in: %w", err)
		}
		return nil
	})
	return a, err
}

// parallel runs fn(0..n-1) on up to workers goroutines and returns the first error.
func parallel(ctx context.Context, n, workers int, fn func(i int) error) error {
	var (
		next     atomic.Int64
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)
	for range min(workers, n) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1) - 1)
				if i >= n || ctx.Err() != nil {
					return
				}
				if err := fn(i); err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
					next.Store(int64(n)) // stop the others
					return
				}
			}
		}()
	}
	wg.Wait()
	if firstErr == nil {
		firstErr = ctx.Err()
	}
	return firstErr
}

// authCall posts a client call from a spread address, as the scenarios do, and decodes the
// answer into out when it is not nil.
func authCall(ctx context.Context, hc *http.Client, u string, body, out any) error {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", u, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", spreadAddr())
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	rb, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", resp.Status, rb)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(rb, out)
}
