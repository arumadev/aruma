// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

// Package adminauth verifies Aruma ID tokens and session cookies and manages Aruma Auth users
// from a Go server that already uses firebase-admin-go. It returns the official
// firebase.google.com/go/v4/auth types, and its errors satisfy the official helpers
// (auth.IsIDTokenExpired, auth.IsIDTokenRevoked, auth.IsUserNotFound...).
//
// The one line a server changes (ADR-013):
//
//	client, err := app.Auth(ctx)                 // before
//	client, err := adminauth.NewClient(ctx, app) // after
//
// Custom tokens stay with the official SDK: CustomToken delegates to app.Auth.
package adminauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	firebase "firebase.google.com/go/v4"
)

// Client is Aruma Auth for one project: the auth.Client methods Aruma answers.
type Client struct {
	app       *firebase.App
	projectID string
	url       string
	http      *http.Client
	tokens    *tokenSource
	idTokens  *verifier
	cookies   *verifier
	now       func() time.Time
}

// Option configures NewClient.
type Option func(*settings)

type settings struct {
	url           string
	keyJSON       []byte
	keyFile       string
	projectID     string
	httpClient    *http.Client
	allowEmulator bool
}

// WithURL sets the Aruma server's public URL. The default is ARUMA_URL, then the origin of the
// key file's token_uri.
func WithURL(u string) Option { return func(s *settings) { s.url = u } }

// WithCredentialsFile reads the service account key file Aruma issued. The default is
// GOOGLE_APPLICATION_CREDENTIALS, the file firebase.NewApp reads by default too.
func WithCredentialsFile(path string) Option { return func(s *settings) { s.keyFile = path } }

// WithCredentialsJSON takes the service account key file's contents.
func WithCredentialsJSON(b []byte) Option { return func(s *settings) { s.keyJSON = b } }

// WithProjectID sets the project; the default is the key file's project_id.
func WithProjectID(id string) Option { return func(s *settings) { s.projectID = id } }

// WithHTTPClient sets the HTTP client for calls to Aruma.
func WithHTTPClient(c *http.Client) Option { return func(s *settings) { s.httpClient = c } }

// AllowEmulatorHost lets NewClient start although FIREBASE_AUTH_EMULATOR_HOST is set. It is
// ignored when GO_ENV or NODE_ENV is "production".
func AllowEmulatorHost() Option { return func(s *settings) { s.allowEmulator = true } }

// serviceAccountKey holds the fields of a key file the library uses.
type serviceAccountKey struct {
	ProjectID    string `json:"project_id"`
	ClientEmail  string `json:"client_email"`
	PrivateKey   string `json:"private_key"`
	PrivateKeyID string `json:"private_key_id"`
	TokenURI     string `json:"token_uri"`
}

// NewClient returns Aruma Auth for the app's project. app is the firebase-admin-go app the
// server already has; it signs custom tokens (CustomToken) and may be nil if those are not
// needed. The service account key, the project and the server come from the options.
func NewClient(ctx context.Context, app *firebase.App, opts ...Option) (*Client, error) {
	var s settings
	for _, o := range opts {
		o(&s)
	}
	if err := emulatorGuard(s.allowEmulator, os.Getenv); err != nil {
		return nil, err
	}
	if _, err := firebaseErrorType(); err != nil {
		return nil, err
	}
	if err := checkParams(); err != nil {
		return nil, err
	}
	key, err := loadKey(s, os.Getenv)
	if err != nil {
		return nil, err
	}
	projectID := s.projectID
	if projectID == "" {
		projectID = key.ProjectID
	}
	if projectID == "" {
		return nil, errors.New("adminauth: no project id: use a key file with project_id or WithProjectID")
	}
	base, err := resolveURL(s.url, key, os.Getenv)
	if err != nil {
		return nil, err
	}
	hc := s.httpClient
	if hc == nil {
		hc = defaultHTTPClient()
	}
	ts, err := newTokenSource(base, key, hc)
	if err != nil {
		return nil, err
	}
	c := &Client{app: app, projectID: projectID, url: base, http: hc, tokens: ts, now: time.Now}
	c.idTokens = newVerifier(base, projectID, idTokenKind, hc)
	c.cookies = newVerifier(base, projectID, sessionCookieKind, hc)
	return c, nil
}

// emulatorGuard refuses to start with FIREBASE_AUTH_EMULATOR_HOST set: with it, the official
// client in the same process skips signature checks and sends "Bearer owner"
// (docs/research/phase2-admin-auth.md).
func emulatorGuard(allow bool, getenv func(string) string) error {
	if getenv("FIREBASE_AUTH_EMULATOR_HOST") == "" {
		return nil
	}
	production := getenv("GO_ENV") == "production" || getenv("NODE_ENV") == "production"
	if allow && !production {
		return nil
	}
	return errors.New("adminauth: FIREBASE_AUTH_EMULATOR_HOST is set: firebase-admin-go would accept unsigned ID tokens " +
		"in this process; unset it, or pass AllowEmulatorHost() outside production")
}

func loadKey(s settings, getenv func(string) string) (serviceAccountKey, error) {
	var k serviceAccountKey
	raw := s.keyJSON
	if raw == nil {
		path := s.keyFile
		if path == "" {
			path = getenv("GOOGLE_APPLICATION_CREDENTIALS")
		}
		if path == "" {
			return k, errors.New("adminauth: no service account key: use WithCredentialsFile or set GOOGLE_APPLICATION_CREDENTIALS")
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return k, fmt.Errorf("adminauth: read the service account key: %w", err)
		}
		raw = b
	}
	if err := json.Unmarshal(raw, &k); err != nil {
		return k, fmt.Errorf("adminauth: parse the service account key: %w", err)
	}
	if k.ClientEmail == "" || k.PrivateKey == "" {
		return k, errors.New("adminauth: the service account key needs client_email and private_key")
	}
	return k, nil
}

const tokenPath = "/oauth2/v1/token"

// resolveURL picks the Aruma public URL: the option, ARUMA_URL, then the key file's token_uri.
// It must be https, except on loopback, and never a Google host: that would mean a Google key.
func resolveURL(opt string, key serviceAccountKey, getenv func(string) string) (string, error) {
	raw := opt
	if raw == "" {
		raw = getenv("ARUMA_URL")
	}
	if raw == "" && strings.HasSuffix(key.TokenURI, tokenPath) {
		raw = strings.TrimSuffix(key.TokenURI, tokenPath)
	}
	if raw == "" {
		return "", errors.New("adminauth: set ARUMA_URL (or WithURL) to your Aruma server, for example https://aruma.example.com")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("adminauth: invalid Aruma URL %q", raw)
	}
	host := strings.ToLower(u.Hostname())
	if host == "google.com" || host == "googleapis.com" || strings.HasSuffix(host, ".google.com") || strings.HasSuffix(host, ".googleapis.com") {
		return "", fmt.Errorf("adminauth: %s is a Google address, not an Aruma server: check the key file and ARUMA_URL", raw)
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback(host)) {
		return "", fmt.Errorf("adminauth: the Aruma URL must use https (plain http is allowed on loopback only): %s", raw)
	}
	if u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", fmt.Errorf("adminauth: the Aruma URL must not carry a query, fragment or credentials: %s", raw)
	}
	return strings.TrimRight(u.Scheme+"://"+u.Host+u.Path, "/"), nil
}

func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// CustomToken is the official custom token, unchanged: Aruma accepts it (ADR-004 §5).
func (c *Client) CustomToken(ctx context.Context, uid string) (string, error) {
	return c.CustomTokenWithClaims(ctx, uid, nil)
}

// CustomTokenWithClaims is the official custom token with developer claims.
func (c *Client) CustomTokenWithClaims(ctx context.Context, uid string, claims map[string]interface{}) (string, error) {
	if c.app == nil {
		return "", errors.New("adminauth: custom tokens need the firebase app passed to NewClient")
	}
	official, err := c.app.Auth(ctx)
	if err != nil {
		return "", err
	}
	return official.CustomTokenWithClaims(ctx, uid, claims)
}

// defaultHTTPClient follows no redirect: one could fetch the key set from another origin, or
// carry the access token there, past the checks on the configured URL. The Node library
// refuses redirects the same way. It keeps enough idle connections for concurrent revocation
// checks: with net/http's default of two per host, a busy server opened a connection per call
// and ran out of local ports.
func defaultHTTPClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConnsPerHost = 64
	return &http.Client{
		Timeout:       30 * time.Second,
		Transport:     t,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}
