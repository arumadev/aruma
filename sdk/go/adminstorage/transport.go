// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

package adminstorage

import (
	"cmp"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// The transport: a service account access token from Aruma's token endpoint (RFC 7523, as
// Google's libraries do it and as adminauth does), put on every request to the Aruma origin.
// The library does not let cloud.google.com/go/auth read the key file: that would ask Google
// for the token unless token_uri is Aruma's, and sign a token of its own when the file has a
// universe_domain other than googleapis.com (credentials/filetypes.go in cloud.google.com/go/auth).

const (
	scope     = "https://www.googleapis.com/auth/devstorage.full_control https://www.googleapis.com/auth/cloud-platform"
	jwtBearer = "urn:ietf:params:oauth:grant-type:jwt-bearer"
)

type tokenSource struct {
	tokenURL string
	key      serviceAccountKey
	signer   *rsa.PrivateKey
	http     *http.Client

	mu      sync.Mutex
	token   string
	refresh time.Time
}

func newTokenSource(base string, key serviceAccountKey, rt http.RoundTripper) (*tokenSource, error) {
	block, _ := pem.Decode([]byte(key.PrivateKey))
	if block == nil {
		return nil, errors.New("adminstorage: the service account private key is not PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		if parsed, err = x509.ParsePKCS1PrivateKey(block.Bytes); err != nil {
			return nil, fmt.Errorf("adminstorage: parse the service account private key: %w", err)
		}
	}
	rk, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("adminstorage: the service account key is not RSA")
	}
	hc := &http.Client{Transport: rt, Timeout: 30 * time.Second, CheckRedirect: noRedirect}
	return &tokenSource{tokenURL: base + tokenPath, key: key, signer: rk, http: hc}, nil
}

func segment(v any) string {
	b, _ := json.Marshal(v)
	return base64.RawURLEncoding.EncodeToString(b)
}

// accessToken returns a cached token until a minute before it expires.
func (t *tokenSource) accessToken(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.token != "" && time.Now().Before(t.refresh) {
		return t.token, nil
	}
	iat := time.Now().Unix()
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	if t.key.PrivateKeyID != "" {
		header["kid"] = t.key.PrivateKeyID
	}
	signing := segment(header) + "." + segment(map[string]any{
		"iss": t.key.ClientEmail, "scope": scope, "aud": t.tokenURL, "iat": iat, "exp": iat + 3600,
	})
	digest := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, t.signer, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	form := url.Values{"grant_type": {jwtBearer}, "assertion": {signing + "." + base64.RawURLEncoding.EncodeToString(sig)}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := t.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("adminstorage: cannot reach the Aruma token endpoint: %w", err)
	}
	defer resp.Body.Close()
	var body struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body)
	if resp.StatusCode != http.StatusOK || body.AccessToken == "" {
		reason := cmp.Or(body.Description, fmt.Sprintf("HTTP %d %s", resp.StatusCode, body.Error))
		return "", errors.New("adminstorage: Aruma refused the service account key: " + reason)
	}
	if body.ExpiresIn <= 0 {
		body.ExpiresIn = 3600
	}
	t.token, t.refresh = body.AccessToken, time.Now().Add(time.Duration(body.ExpiresIn-60)*time.Second)
	return t.token, nil
}

// baseTransport is what carries the requests: the transport of the WithHTTPClient client, or a
// clone of http.DefaultTransport.
func baseTransport(c *http.Client) http.RoundTripper {
	if c != nil && c.Transport != nil {
		return c.Transport
	}
	return http.DefaultTransport.(*http.Transport).Clone()
}

// authorizedClient returns the client every Storage request goes through: rt under
// authTransport, with no redirect followed. Without WithHTTPClient it has no overall timeout,
// which would cut a large upload or download short; contexts bound the calls.
func authorizedClient(origin string, ts *tokenSource, base *http.Client, rt http.RoundTripper) *http.Client {
	c := &http.Client{}
	if base != nil {
		*c = *base
	}
	c.Transport = &authTransport{origin: origin, tokens: ts, base: rt}
	c.CheckRedirect = noRedirect
	return c
}

func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// authTransport adds the access token to requests for the Aruma origin, and refuses any other:
// the client knows no other server, and the token must never reach one (firebase-admin's
// getDownloadURL sends it to firebasestorage.googleapis.com).
// A resumable session's URL is the server's public URL, so WithURL must be that URL.
type authTransport struct {
	origin string
	tokens *tokenSource
	base   http.RoundTripper
}

func (t *authTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if got := origin(r.URL); got != t.origin {
		closeBody(r)
		return nil, fmt.Errorf("adminstorage: refusing a request to %s: this client talks to %s only "+
			"(an upload session on another origin means the Aruma URL is not the server's public_url)", got, t.origin)
	}
	token, err := t.tokens.accessToken(r.Context())
	if err != nil {
		closeBody(r)
		return nil, err
	}
	out := r.Clone(r.Context())
	out.Header.Set("Authorization", "Bearer "+token)
	return t.base.RoundTrip(out)
}

// closeBody closes the request body, as a RoundTripper must on every path.
func closeBody(r *http.Request) {
	if r.Body != nil {
		_ = r.Body.Close()
	}
}
