package core

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/osama300/aurma-project/internal/identity/tokens"
)

// Provider ID tokens are verified with the provider's published keys, cached for as long as
// the provider's Cache-Control allows (between 5 minutes and a day). An unknown kid triggers
// one refetch, at most once a minute, so rotated keys are picked up without hammering the
// provider.

const (
	jwksMinTTL     = 5 * time.Minute
	jwksMaxTTL     = 24 * time.Hour
	jwksRefetchGap = time.Minute
	jwksMaxBytes   = 1 << 20
)

type jwksEntry struct {
	keys      map[string]*rsa.PublicKey
	expires   time.Time
	fetchedAt time.Time
}

type jwksCache struct {
	client *http.Client
	now    func() time.Time

	mu      sync.Mutex
	entries map[string]*jwksEntry
}

func newJWKSCache(client *http.Client) *jwksCache {
	return &jwksCache{client: client, now: time.Now, entries: map[string]*jwksEntry{}}
}

// key returns the provider key kid from the JWKS at url.
func (c *jwksCache) key(ctx context.Context, url, kid string) (*rsa.PublicKey, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	e := c.entries[url]
	if e != nil && now.Before(e.expires) {
		if k := e.keys[kid]; k != nil {
			return k, nil
		}
		if now.Sub(e.fetchedAt) < jwksRefetchGap {
			return nil, tokens.ErrSignature
		}
	}
	fresh, err := c.fetch(ctx, url)
	if err != nil {
		if e != nil && e.keys[kid] != nil {
			return e.keys[kid], nil // the provider is unreachable: keep using a key we know
		}
		return nil, err
	}
	c.entries[url] = fresh
	if k := fresh.keys[kid]; k != nil {
		return k, nil
	}
	return nil, tokens.ErrSignature
}

func (c *jwksCache) fetch(ctx context.Context, url string) (*jwksEntry, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("provider keys: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("provider keys: HTTP %d", resp.StatusCode)
	}
	var set struct {
		Keys []tokens.JWK `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, jwksMaxBytes)).Decode(&set); err != nil {
		return nil, fmt.Errorf("provider keys: %w", err)
	}
	now := c.now()
	e := &jwksEntry{keys: map[string]*rsa.PublicKey{}, fetchedAt: now, expires: now.Add(cacheTTL(resp.Header.Get("Cache-Control")))}
	for _, k := range set.Keys {
		if k.Kty != "RSA" || k.Kid == "" || (k.Alg != "" && k.Alg != "RS256") || (k.Use != "" && k.Use != "sig") {
			continue
		}
		pub, err := k.PublicKey()
		if err != nil || pub.N.BitLen() < 2048 || pub.E < 3 || pub.E%2 == 0 {
			continue
		}
		e.keys[k.Kid] = pub
	}
	return e, nil
}

// cacheTTL reads max-age from a Cache-Control header, clamped to the cache bounds.
func cacheTTL(header string) time.Duration {
	ttl := time.Hour
	for _, part := range strings.Split(header, ",") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(part), "max-age="); ok {
			if n, err := strconv.Atoi(v); err == nil {
				ttl = time.Duration(n) * time.Second
			}
		}
	}
	return min(max(ttl, jwksMinTTL), jwksMaxTTL)
}

// providerClaims are the claims Aruma reads from a Google or Apple ID token.
type providerClaims struct {
	Iss           string   `json:"iss"`
	Aud           audience `json:"aud"`
	Azp           string   `json:"azp"`
	Sub           string   `json:"sub"`
	Exp           int64    `json:"exp"`
	Iat           int64    `json:"iat"`
	Nonce         string   `json:"nonce"`
	Email         string   `json:"email"`
	EmailVerified flexBool `json:"email_verified"`
	Name          string   `json:"name"`
	GivenName     string   `json:"given_name"`
	FamilyName    string   `json:"family_name"`
	Picture       string   `json:"picture"`

	raw json.RawMessage
}

// audience is a JWT aud: one string or a list.
type audience []string

func (a *audience) UnmarshalJSON(b []byte) error {
	var one string
	if json.Unmarshal(b, &one) == nil {
		*a = audience{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*a = many
	return nil
}

// flexBool accepts true and "true": Apple sends email_verified as a string.
type flexBool bool

func (f *flexBool) UnmarshalJSON(b []byte) error {
	switch string(b) {
	case `true`, `"true"`:
		*f = true
	default:
		*f = false
	}
	return nil
}

// errIdPToken is the error for a provider token that fails any check. The detail says which,
// for the developer; it never echoes the token.
func errIdPToken(detail string) error { return failf("INVALID_IDP_RESPONSE", detail) }

// clockSkew is the leeway allowed for provider clocks.
const clockSkew = 60 * time.Second

// verifyProviderToken checks a provider ID token: signature with the provider's keys, issuer,
// audience (the project's client ID, never another client's), expiry and issue time. The
// nonce is the caller's to check, since what it must equal depends on the flow.
func (s *Service) verifyProviderToken(ctx context.Context, idp *idpCreds, token string) (*providerClaims, error) {
	kid, err := tokens.KidOf(token)
	if err != nil {
		return nil, errIdPToken("the id_token is not an RS256 JWT")
	}
	pub, err := idp.keys.key(ctx, idp.endpoints.JWKSURL, kid)
	if err != nil {
		if errors.Is(err, tokens.ErrSignature) {
			return nil, errIdPToken("the id_token is not signed by " + idp.provider)
		}
		s.log.Warn("idp keys", "provider", idp.provider, "error", err)
		return nil, errIdPToken("the provider's keys could not be fetched")
	}
	payload, err := tokens.VerifyWith(token, pub)
	if err != nil {
		return nil, errIdPToken("the id_token is not signed by " + idp.provider)
	}
	var c providerClaims
	if json.Unmarshal(payload, &c) != nil {
		return nil, errIdPToken("the id_token claims are malformed")
	}
	c.raw = payload
	now := s.now()
	switch {
	case !slices.Contains(idp.endpoints.Issuers, c.Iss):
		return nil, errIdPToken("the id_token was not issued by " + idp.provider)
	case idp.clientID == "" || !slices.Contains(c.Aud, idp.clientID) || (len(c.Aud) > 1 && c.Azp != idp.clientID):
		return nil, errIdPToken("the id_token was issued for another client")
	case c.Sub == "" || len(c.Sub) > 255:
		return nil, errIdPToken("the id_token has no subject")
	case c.Exp == 0 || now.After(time.Unix(c.Exp, 0).Add(clockSkew)):
		return nil, errIdPToken("the id_token has expired")
	case c.Iat > now.Add(clockSkew).Unix():
		return nil, errIdPToken("the id_token is issued in the future")
	}
	return &c, nil
}
