// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

package adminauth

import (
	"bytes"
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

// The Admin API transport: a service account access token from Aruma's token endpoint
// (RFC 7523, as Google's libraries do it), then the Identity Toolkit calls firebase-admin-go
// makes, sent to Aruma.

const (
	scope     = "https://www.googleapis.com/auth/cloud-platform"
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

func newTokenSource(base string, key serviceAccountKey, hc *http.Client) (*tokenSource, error) {
	block, _ := pem.Decode([]byte(key.PrivateKey))
	if block == nil {
		return nil, errors.New("adminauth: the service account private key is not PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		if parsed, err = x509.ParsePKCS1PrivateKey(block.Bytes); err != nil {
			return nil, fmt.Errorf("adminauth: parse the service account private key: %w", err)
		}
	}
	rk, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("adminauth: the service account key is not RSA")
	}
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
		return "", newError(platformUnauthenticated, "", "cannot reach the Aruma token endpoint: "+err.Error())
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
		reason := body.Description
		if reason == "" {
			reason = fmt.Sprintf("HTTP %d %s", resp.StatusCode, body.Error)
		}
		return "", newError(platformUnauthenticated, "", "Aruma refused the service account key: "+reason)
	}
	if body.ExpiresIn <= 0 {
		body.ExpiresIn = 3600
	}
	t.token, t.refresh = body.AccessToken, time.Now().Add(time.Duration(body.ExpiresIn-60)*time.Second)
	return t.token, nil
}

// call sends an Admin API request. path is relative to /v1/projects/{project}, e.g.
// "/accounts:lookup"; out may be nil.
func (c *Client) call(ctx context.Context, method, path string, in, out any) error {
	token, err := c.tokens.accessToken(ctx)
	if err != nil {
		return err
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	u := c.url + "/identitytoolkit.googleapis.com/v1/projects/" + url.PathEscape(c.projectID) + path
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return newError(platformUnavailable, "", "cannot reach Aruma Auth: "+err.Error())
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(b, &e)
		return responseError(resp.StatusCode, e.Error.Message)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("adminauth: parse the response of %s: %w", path, err)
	}
	return nil
}
