// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

package adminauth

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"firebase.google.com/go/v4/auth"
)

// Aruma ID tokens and session cookies (ADR-004) are verified against the keys their issuer
// publishes through OpenID discovery. Only RS256 with a kid is accepted, so unsigned tokens,
// "alg: none" and HMAC confusion fail before any key is looked at. Time checks follow
// firebase-admin-go: 300 seconds of skew for exp and iat; auth_time must be present too.

const (
	clockSkew       = 300 // seconds
	refetchCooldown = 30 * time.Second
	customTokenAud  = "https://identitytoolkit.googleapis.com/google.identity.identitytoolkit.v1.IdentityToolkit"
)

// tokenKind names a kind of token and its error codes.
type tokenKind struct {
	name, prefix              string
	invalid, expired, revoked string
}

var (
	idTokenKind       = tokenKind{"ID token", "/issuers/", codeIDTokenInvalid, codeIDTokenExpired, codeIDTokenRevoked}
	sessionCookieKind = tokenKind{"session cookie", "/session-issuers/", codeSessionInvalid, codeSessionExpired, codeSessionRevoked}
)

type verifier struct {
	kind      tokenKind
	projectID string
	issuer    string
	origin    string
	http      *http.Client
	now       func() time.Time

	mu        sync.Mutex
	keys      map[string]*rsa.PublicKey
	fetchedAt time.Time
	expiresAt time.Time
}

func newVerifier(base, projectID string, kind tokenKind, hc *http.Client) *verifier {
	u, _ := url.Parse(base)
	return &verifier{
		kind: kind, projectID: projectID, issuer: base + kind.prefix + projectID,
		origin: u.Scheme + "://" + u.Host, http: hc, now: time.Now,
	}
}

func (v *verifier) invalid(format string, args ...any) error {
	return newError(platformInvalidArgument, v.kind.invalid, v.kind.name+" "+fmt.Sprintf(format, args...))
}

// verify checks the token and returns it as the official auth.Token.
func (v *verifier) verify(ctx context.Context, token string) (*auth.Token, error) {
	if token == "" {
		return nil, v.invalid("must be a non-empty string")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, v.invalid("is not a JWT")
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	var claims map[string]any
	var t auth.Token
	if decode(parts[0], &header) != nil || decode(parts[1], &claims) != nil || decode(parts[1], &t) != nil {
		return nil, v.invalid("is not a JWT")
	}
	if header.Kid == "" {
		if claims["aud"] == customTokenAud {
			return nil, v.invalid("expected, but a custom token was given")
		}
		return nil, v.invalid("has no 'kid' header")
	}
	if header.Alg != "RS256" {
		return nil, v.invalid("has algorithm %q; expected RS256", header.Alg)
	}
	if t.Audience != v.projectID {
		return nil, v.invalid("has audience %q; expected %q", t.Audience, v.projectID)
	}
	if t.Issuer != v.issuer {
		return nil, v.invalid("has issuer %q; expected %q", t.Issuer, v.issuer)
	}
	if t.Subject == "" || len(t.Subject) > 128 {
		return nil, v.invalid("has an invalid 'sub' claim")
	}
	for _, c := range []string{"iat", "exp", "auth_time"} {
		if _, ok := claims[c].(float64); !ok {
			return nil, v.invalid("has no numeric %q claim", c)
		}
	}
	now := v.now().Unix()
	if t.IssuedAt-clockSkew > now || t.AuthTime-clockSkew > now {
		return nil, v.invalid("was issued in the future")
	}
	key, err := v.key(ctx, header.Kid)
	if err != nil {
		return nil, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err != nil || rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], sig) != nil {
		return nil, v.invalid("has an invalid signature")
	}
	// Expiry is reported only for a token that is otherwise genuine.
	if t.Expires+clockSkew < now {
		return nil, newError(platformInvalidArgument, v.kind.expired, v.kind.name+" has expired")
	}
	t.UID = t.Subject
	for _, c := range []string{"iss", "aud", "exp", "iat", "sub", "uid"} {
		delete(claims, c)
	}
	t.Claims = claims
	return &t, nil
}

func decode(seg string, out any) error {
	b, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func (v *verifier) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	now := v.now()
	if v.keys == nil || !now.Before(v.expiresAt) || (v.keys[kid] == nil && now.Sub(v.fetchedAt) > refetchCooldown) {
		if err := v.load(ctx); err != nil {
			return nil, newError(platformUnknown, codeCertificateFetch, "cannot fetch the public keys of "+v.issuer+": "+err.Error())
		}
	}
	k := v.keys[kid]
	if k == nil {
		return nil, v.invalid("has 'kid' %q, which is not a published key of %s", kid, v.issuer)
	}
	return k, nil
}

// load reads the issuer's discovery document, then its JWKS. Both must come from the Aruma
// server the client was configured with.
func (v *verifier) load(ctx context.Context) error {
	var disc struct {
		Issuer  string `json:"issuer"`
		JWKSURI string `json:"jwks_uri"`
	}
	if _, err := v.getJSON(ctx, v.issuer+"/.well-known/openid-configuration", &disc); err != nil {
		return err
	}
	u, err := url.Parse(disc.JWKSURI)
	if disc.Issuer != v.issuer || err != nil || u.Scheme+"://"+u.Host != v.origin {
		return errors.New("the discovery document does not describe this issuer")
	}
	var jwks struct {
		Keys []struct {
			Kty, Alg, Use, Kid, N, E string
		} `json:"keys"`
	}
	maxAge, err := v.getJSON(ctx, disc.JWKSURI, &jwks)
	if err != nil {
		return err
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range jwks.Keys {
		if k.Kty != "RSA" || k.Kid == "" || (k.Alg != "" && k.Alg != "RS256") || (k.Use != "" && k.Use != "sig") {
			continue
		}
		n, errN := base64.RawURLEncoding.DecodeString(k.N)
		e, errE := base64.RawURLEncoding.DecodeString(k.E)
		if errN != nil || errE != nil || len(e) == 0 || len(e) > 4 {
			continue
		}
		keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	}
	now := v.now()
	v.keys, v.fetchedAt, v.expiresAt = keys, now, now.Add(maxAge)
	return nil
}

var maxAgeRe = regexp.MustCompile(`max-age=(\d+)`)

func (v *verifier) getJSON(ctx context.Context, u string, out any) (time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	resp, err := v.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HTTP %d from %s", resp.StatusCode, u)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out); err != nil {
		return 0, err
	}
	var maxAge time.Duration
	if m := maxAgeRe.FindStringSubmatch(resp.Header.Get("Cache-Control")); m != nil {
		s, _ := strconv.Atoi(m[1])
		maxAge = time.Duration(s) * time.Second
	}
	return maxAge, nil
}

// VerifyIDToken verifies an Aruma ID token's signature and claims.
func (c *Client) VerifyIDToken(ctx context.Context, idToken string) (*auth.Token, error) {
	return c.idTokens.verify(ctx, idToken)
}

// VerifyIDTokenAndCheckRevoked also checks that the user is enabled and the token was issued
// after the user's sessions were last revoked.
func (c *Client) VerifyIDTokenAndCheckRevoked(ctx context.Context, idToken string) (*auth.Token, error) {
	return c.verifyAndCheck(ctx, c.idTokens, idToken)
}

// VerifySessionCookie verifies an Aruma session cookie's signature and claims.
func (c *Client) VerifySessionCookie(ctx context.Context, cookie string) (*auth.Token, error) {
	return c.cookies.verify(ctx, cookie)
}

// VerifySessionCookieAndCheckRevoked also checks the user and revocation, as
// VerifyIDTokenAndCheckRevoked.
func (c *Client) VerifySessionCookieAndCheckRevoked(ctx context.Context, cookie string) (*auth.Token, error) {
	return c.verifyAndCheck(ctx, c.cookies, cookie)
}

// verifyAndCheck verifies, then compares iat with TokensValidAfterMillis, as
// firebase-admin-go does.
func (c *Client) verifyAndCheck(ctx context.Context, v *verifier, token string) (*auth.Token, error) {
	t, err := v.verify(ctx, token)
	if err != nil {
		return nil, err
	}
	u, err := c.GetUser(ctx, t.UID)
	if err != nil {
		return nil, err
	}
	if u.Disabled {
		return nil, newError(platformInvalidArgument, codeUserDisabled, "user has been disabled")
	}
	if t.IssuedAt*1000 < u.TokensValidAfterMillis {
		return nil, newError(platformInvalidArgument, v.kind.revoked, v.kind.name+" has been revoked")
	}
	return t, nil
}
