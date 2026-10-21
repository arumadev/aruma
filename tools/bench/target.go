package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arumadev/aruma/internal/app"
	"github.com/arumadev/aruma/internal/config"
	"github.com/arumadev/aruma/internal/platform/authn"
	acrypto "github.com/arumadev/aruma/internal/platform/crypto"
	alog "github.com/arumadev/aruma/internal/platform/log"
	"github.com/arumadev/aruma/internal/store"
	"github.com/arumadev/aruma/internal/store/storetest"
	"github.com/arumadev/aruma/internal/tenancy/core"
)

// Target is a running Aruma and the credentials to call it.
type Target struct {
	URL      string // base URL, no trailing slash
	Token    string // personal CLI token (aruma_pat_…)
	Email    string // console account, for the session and login scenarios
	Password string
	Pool     *pgxpool.Pool // in-process only: the server's pool, for acquire statistics
	close    func()
}

// Close stops an in-process server and drops its database.
func (t *Target) Close() {
	if t.close != nil {
		t.close()
	}
}

// StartInProcess creates a throwaway database, migrates it, and serves Aruma on a loopback
// port from this process, wired exactly as `aruma serve` wires it: production argon2id cost,
// the JSON request log (written to io.Discard, so formatting is paid but nothing is kept), and
// loopback trusted as a proxy so the bench can spread credential requests over many client
// addresses with X-Forwarded-For (see Scenario.SpreadClients). poolMax, when positive, sizes
// the server's database pool; pgx's default is max(4, NumCPU).
func StartInProcess(ctx context.Context, logOut io.Writer, poolMax int) (*Target, error) {
	dbName := "aruma_bench_" + randomHex(4)
	admin, err := pgx.Connect(ctx, storetest.AdminURL())
	if err != nil {
		return nil, fmt.Errorf("connect to Postgres (ARUMA_TEST_DATABASE_URL): %w", err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+dbName); err != nil {
		admin.Close(ctx)
		return nil, err
	}
	var cleanups []func()
	cleanup := func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}
	cleanups = append(cleanups, func() {
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = admin.Exec(c, "DROP DATABASE IF EXISTS "+dbName+" WITH (FORCE)")
		admin.Close(c)
	})
	fail := func(err error) (*Target, error) { cleanup(); return nil, err }

	u, _ := url.Parse(storetest.AdminURL())
	u.Path = "/" + dbName
	if err := store.Migrate(ctx, u.String()); err != nil {
		return fail(err)
	}
	if poolMax > 0 {
		// The knob an operator has: pgxpool reads pool_max_conns from ARUMA_DATABASE_URL. Only
		// the pool gets it; store.Migrate (database/sql) would send it to Postgres as a
		// runtime parameter, which Postgres rejects.
		q := u.Query()
		q.Set("pool_max_conns", fmt.Sprint(poolMax))
		u.RawQuery = q.Encode()
	}
	dbURL := u.String()
	pool, err := store.Open(ctx, dbURL)
	if err != nil {
		return fail(err)
	}
	cleanups = append(cleanups, pool.Close)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fail(err)
	}
	base := "http://" + ln.Addr().String()
	vars := map[string]string{
		"ARUMA_DATABASE_URL":    dbURL,
		"ARUMA_PUBLIC_URL":      base,
		"ARUMA_MASTER_KEY":      base64.StdEncoding.EncodeToString(acrypto.RandomBytes(32)),
		"ARUMA_TRUSTED_PROXIES": "127.0.0.1/32,::1/128",
	}
	cfg, err := config.Load("", func(k string) string { return vars[k] })
	if err != nil {
		return fail(err)
	}
	logger := alog.New(logOut, "info")
	a, err := app.New(cfg, logger, "bench", pool)
	if err != nil {
		return fail(err)
	}
	// The same timeouts server.Run sets on the public listener.
	srv := &http.Server{
		Handler:           a.Server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}
	go func() { _ = srv.Serve(ln) }()
	cleanups = append(cleanups, func() { _ = srv.Close() })

	t := &Target{URL: base, Email: "bench@example.com", Password: "bench-" + randomHex(12), Pool: pool, close: cleanup}
	acc, err := a.Service.CreateAccount(ctx, core.NewAccount{Email: t.Email, Password: t.Password})
	if err != nil {
		return fail(err)
	}
	tok, err := a.Service.CreateAccessToken(ctx, &authn.Principal{Kind: authn.KindAccount, AccountID: acc.ID}, "bench", 0)
	if err != nil {
		return fail(err)
	}
	t.Token = tok.Plaintext
	return t, nil
}

// Fixture is the data the scenarios read, created through the public APIs.
type Fixture struct {
	Projects   []string // project ids
	WebApps    []string // one web app id per project
	Cookie     string   // "name=value" of a console session
	Assertions []string // signed service account JWTs, valid for an hour
	TokenURL   string   // where to POST them
	Auth       *AuthFixture
}

// Needs says which optional parts of the fixture the selected scenarios use.
type Needs struct {
	Session bool // a console session cookie
	SA      bool // a service account key in the first project
	Auth    bool // Auth users in the first project (AuthFixture)
	// AuthUsers is how many email/password users to create when Auth is set.
	AuthUsers int
}

// Seed creates n projects, each with a web, an Android and an iOS app, a service account key in
// the first, Auth users in the first, and signs in once for a console session. Everything goes
// through the HTTP APIs with the CLI token, as a developer would, so it works against any
// server.
func Seed(ctx context.Context, hc *http.Client, t *Target, n int, need Needs) (*Fixture, error) {
	c := &apiClient{hc: hc, base: t.URL + "/console/v1", token: t.Token}
	f := &Fixture{}
	run := randomHex(3)
	var firstKeyID string // the API key of the first project's web app
	for i := range n {
		id := fmt.Sprintf("bench-%s-%03d", run, i)
		if err := c.json(ctx, "POST", "/projects", map[string]string{"projectId": id, "displayName": "Bench " + id}, nil); err != nil {
			return nil, fmt.Errorf("create project %s: %w", id, err)
		}
		f.Projects = append(f.Projects, id)
		ns := fmt.Sprintf("com.bench.p%s%03d", run, i)
		var web struct {
			AppID    string `json:"appId"`
			APIKeyID string `json:"apiKeyId"`
		}
		apps := []struct {
			body map[string]string
			out  any
		}{
			{map[string]string{"platform": "WEB", "displayName": "Web"}, &web},
			{map[string]string{"platform": "ANDROID", "displayName": "Android", "packageName": ns}, nil},
			{map[string]string{"platform": "IOS", "displayName": "iOS", "bundleId": ns}, nil},
		}
		for _, a := range apps {
			if err := c.json(ctx, "POST", "/projects/"+id+"/apps", a.body, a.out); err != nil {
				return nil, fmt.Errorf("create %s app in %s: %w", a.body["platform"], id, err)
			}
		}
		f.WebApps = append(f.WebApps, web.AppID)
		if i == 0 {
			firstKeyID = web.APIKeyID
		}
	}

	var keyFile *serviceAccountKey
	if need.SA && n > 0 {
		var sa struct {
			ID string `json:"id"`
		}
		if err := c.json(ctx, "POST", "/projects/"+f.Projects[0]+"/serviceAccounts", map[string]string{"accountId": "bench-sa-" + run}, &sa); err != nil {
			return nil, fmt.Errorf("create service account: %w", err)
		}
		raw, err := c.raw(ctx, "POST", "/projects/"+f.Projects[0]+"/serviceAccounts/"+sa.ID+"/keys", nil)
		if err != nil {
			return nil, fmt.Errorf("create service account key: %w", err)
		}
		if keyFile, err = parseKeyFile(raw); err != nil {
			return nil, err
		}
		if f.Assertions, err = signAssertions(keyFile, 32); err != nil {
			return nil, err
		}
		f.TokenURL = t.URL + "/oauth2/v1/token"
	}

	if need.Auth && n > 0 {
		var err error
		if f.Auth, err = seedAuth(ctx, hc, c, t.URL, f.Projects[0], firstKeyID, run, need.AuthUsers, keyFile); err != nil {
			return nil, err
		}
	}

	if need.Session {
		cookie, err := login(ctx, hc, t, "")
		if err != nil {
			return nil, fmt.Errorf("console login: %w", err)
		}
		f.Cookie = cookie
	}
	return f, nil
}

// login signs in and returns the session cookie as "name=value".
func login(ctx context.Context, hc *http.Client, t *Target, forwardedFor string) (string, error) {
	b, _ := json.Marshal(map[string]string{"email": t.Email, "password": t.Password})
	req, err := http.NewRequestWithContext(ctx, "POST", t.URL+"/console/v1/auth:login", bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if forwardedFor != "" {
		req.Header.Set("X-Forwarded-For", forwardedFor)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: %s", resp.Status, body)
	}
	// Read the cookie by hand: on loopback it may be Secure, which a cookie jar would not send
	// back over plain http.
	for _, ck := range resp.Cookies() {
		if strings.HasSuffix(ck.Name, "aruma_session") && ck.Value != "" {
			return ck.Name + "=" + ck.Value, nil
		}
	}
	return "", errors.New("no session cookie in the login response")
}

// serviceAccountKey is a downloaded service account key file, parsed.
type serviceAccountKey struct {
	ID       string // private_key_id, the JWT kid
	Email    string // client_email
	TokenURI string // token_uri
	Key      *rsa.PrivateKey
}

func parseKeyFile(keyFile []byte) (*serviceAccountKey, error) {
	var kf struct {
		PrivateKeyID string `json:"private_key_id"`
		PrivateKey   string `json:"private_key"`
		ClientEmail  string `json:"client_email"`
		TokenURI     string `json:"token_uri"`
	}
	if err := json.Unmarshal(keyFile, &kf); err != nil {
		return nil, fmt.Errorf("key file: %w", err)
	}
	block, _ := pem.Decode([]byte(kf.PrivateKey))
	if block == nil {
		return nil, errors.New("key file: no PEM private key")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("key file: %w", err)
	}
	key, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("key file: not an RSA key")
	}
	return &serviceAccountKey{ID: kf.PrivateKeyID, Email: kf.ClientEmail, TokenURI: kf.TokenURI, Key: key}, nil
}

// sign returns claims as an RS256 JWT signed with the key, the way Google's libraries sign.
func (k *serviceAccountKey) sign(claims map[string]any) (string, error) {
	enc := base64.RawURLEncoding
	hdr, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": k.ID})
	body, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signing := enc.EncodeToString(hdr) + "." + enc.EncodeToString(body)
	digest := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, k.Key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return signing + "." + enc.EncodeToString(sig), nil
}

// signAssertions signs n RS256 JWT bearer assertions (RFC 7523) with a downloaded key file, the
// way Google's client libraries do. They are signed up front so the bench's own RSA signing
// does not compete with the server for CPU; the server does not reject replays, so reusing
// them is valid.
func signAssertions(k *serviceAccountKey, n int) ([]string, error) {
	out := make([]string, n)
	now := time.Now().Unix()
	for i := range out {
		var err error
		out[i], err = k.sign(map[string]any{
			"iss": k.Email, "sub": k.Email, "aud": k.TokenURI,
			"scope": "https://www.googleapis.com/auth/cloud-platform",
			"iat":   now, "exp": now + 3600, "jti": randomHex(8),
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

type apiClient struct {
	hc          *http.Client
	base, token string
}

func (c *apiClient) raw(ctx context.Context, method, path string, body any) ([]byte, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, b)
	}
	return b, nil
}

func (c *apiClient) json(ctx context.Context, method, path string, body, out any) error {
	b, err := c.raw(ctx, method, path, body)
	if err != nil || out == nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
