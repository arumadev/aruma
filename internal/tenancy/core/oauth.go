package core

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math"
	"strings"
	"time"

	"github.com/arumadev/aruma/internal/platform/authn"
	acrypto "github.com/arumadev/aruma/internal/platform/crypto"
	"github.com/arumadev/aruma/internal/tenancy/queries"
)

// Service account tokens follow RFC 7523 (JWT bearer grant) as Google's libraries speak it.
const (
	ServiceTokenLifetime = time.Hour
	clockSkew            = 60 * time.Second
	maxAssertionLifetime = time.Hour
)

// OAuthError is an RFC 6749 error response.
type OAuthError struct {
	Code        string `json:"error"`
	Description string `json:"error_description"`
}

func (e *OAuthError) Error() string { return e.Code + ": " + e.Description }

func invalidGrant(desc string) *OAuthError {
	return &OAuthError{Code: "invalid_grant", Description: desc}
}

// TokenResponse is a successful token response.
type TokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	TokenType   string `json:"token_type"`
}

type jwtHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
}

type jwtClaims struct {
	Iss   string          `json:"iss"`
	Sub   string          `json:"sub"`
	Aud   json.RawMessage `json:"aud"`
	Scope string          `json:"scope"`
	Iat   float64         `json:"iat"`
	Exp   float64         `json:"exp"`
}

// ExchangeAssertion verifies a service account's signed JWT and issues a one-hour access token.
// tokenURL is this endpoint's own address, which the assertion's audience must name.
func (s *Service) ExchangeAssertion(ctx context.Context, assertion, tokenURL string) (*TokenResponse, error) {
	parts := strings.Split(assertion, ".")
	if len(parts) != 3 {
		return nil, invalidGrant("The assertion is not a JWT.")
	}
	var h jwtHeader
	var c jwtClaims
	if err := decodeSegment(parts[0], &h); err != nil {
		return nil, invalidGrant("The JWT header is malformed.")
	}
	if h.Alg != "RS256" {
		// Never "none", never HMAC with a public key as the secret (ADR-010 S-3).
		s.log.Warn("rejected assertion with unsupported alg", "alg", clip(h.Alg, 16))
		return nil, invalidGrant("Only RS256 assertions are accepted.")
	}
	if err := decodeSegment(parts[1], &c); err != nil {
		return nil, invalidGrant("The JWT claims are malformed.")
	}
	if c.Iss == "" || !audienceMatches(c.Aud, tokenURL) {
		return nil, invalidGrant("Invalid JWT: iss is missing or aud is not this token endpoint.")
	}
	now := time.Now()
	iat, exp := unix(c.Iat), unix(c.Exp)
	switch {
	case iat.After(now.Add(clockSkew)):
		return nil, invalidGrant("Invalid JWT: iat is in the future.")
	case !exp.After(now.Add(-clockSkew)):
		return nil, invalidGrant("Invalid JWT: the assertion has expired.")
	case !exp.After(iat) || exp.Sub(iat) > maxAssertionLifetime+clockSkew:
		return nil, invalidGrant("Invalid JWT: exp must be after iat and at most one hour later.")
	}

	// The signature is checked before anything about the account is revealed: without its key,
	// an unknown, disabled or deleted account all look the same.
	keys, err := s.q.ListActiveKeysByEmail(ctx, c.Iss)
	if err != nil {
		return nil, internal(err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, errBadAssertion
	}
	keyID, ok := verifyWithAny(keys, h.Kid, digest[:], sig)
	if !ok {
		return nil, errBadAssertion
	}
	sa, err := s.q.GetServiceAccountByEmail(ctx, c.Iss)
	if err != nil {
		if notFound(err) {
			return nil, errBadAssertion
		}
		return nil, internal(err)
	}
	if sa.Disabled || sa.ProjectState != "ACTIVE" {
		return nil, invalidGrant("The service account is disabled or its project is deleted.")
	}

	token := acrypto.NewToken(authn.ServiceTokenPrefix)
	scopes := strings.Fields(c.Scope)
	if scopes == nil {
		scopes = []string{}
	}
	err = s.q.CreateOAuthToken(ctx, queries.CreateOAuthTokenParams{
		TokenHash: s.keys.HashToken(token), PrincipalID: sa.ID, ProjectNumber: sa.ProjectNumber,
		Scopes: scopes, ExpiresAt: now.Add(ServiceTokenLifetime), KeyID: &keyID,
	})
	if err != nil {
		return nil, internal(err)
	}
	return &TokenResponse{AccessToken: token, ExpiresIn: int(ServiceTokenLifetime.Seconds()), TokenType: "Bearer"}, nil
}

var errBadAssertion = invalidGrant("Invalid JWT signature.")

// verifyWithAny checks the signature with the key named by kid, or, when the client sent no
// kid (Node's gtoken does not), with each active key in turn. It returns the verifying key.
func verifyWithAny(keys []queries.ControlServiceAccountKey, kid string, digest, sig []byte) (string, bool) {
	for _, k := range keys {
		if kid != "" && k.KeyID != kid {
			continue
		}
		block, _ := pem.Decode([]byte(k.CertificatePem))
		if block == nil {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			continue
		}
		pub, ok := cert.PublicKey.(*rsa.PublicKey)
		if ok && rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest, sig) == nil {
			return k.KeyID, true
		}
	}
	return "", false
}

func decodeSegment(seg string, v any) error {
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(seg, "="))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func audienceMatches(raw json.RawMessage, want string) bool {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return one == want
	}
	var many []string
	if json.Unmarshal(raw, &many) == nil {
		for _, a := range many {
			if a == want {
				return true
			}
		}
	}
	return false
}

func unix(f float64) time.Time {
	sec, frac := math.Modf(f)
	return time.Unix(int64(sec), int64(frac*1e9))
}

// verifyServiceToken resolves a service account access token.
func (s *Service) verifyServiceToken(ctx context.Context, token string) (*authn.Principal, error) {
	row, err := s.q.GetActiveOAuthToken(ctx, s.keys.HashToken(token))
	if err != nil {
		if notFound(err) {
			return nil, errInvalidCredential
		}
		return nil, internal(err)
	}
	if row.AccountDisabled || row.ProjectState != "ACTIVE" {
		return nil, errInvalidCredential
	}
	return &authn.Principal{
		Kind: authn.KindServiceAccount, Method: authn.MethodServiceToken,
		ServiceAccountID: row.PrincipalID, ProjectNumber: row.ProjectNumber, Email: row.Email, ExpiresAt: row.ExpiresAt,
	}, nil
}

// ExchangeRefreshToken serves the firebase CLI, which sends FIREBASE_TOKEN as an OAuth refresh
// token to FIREBASE_TOKEN_URL. A valid personal CLI token is returned as its own access token.
func (s *Service) ExchangeRefreshToken(ctx context.Context, refreshToken string) (*TokenResponse, error) {
	if !strings.HasPrefix(refreshToken, authn.AccessTokenPrefix) {
		return nil, invalidGrant("The token is not an Aruma CLI token.")
	}
	if _, err := s.VerifyAccessToken(ctx, refreshToken); err != nil {
		return nil, invalidGrant("The CLI token is invalid, expired or revoked.")
	}
	return &TokenResponse{AccessToken: refreshToken, ExpiresIn: int(ServiceTokenLifetime.Seconds()), TokenType: "Bearer"}, nil
}

// PurgeExpiredTokens removes long-expired service account tokens.
func (s *Service) PurgeExpiredTokens(ctx context.Context) (int64, error) {
	n, err := s.q.DeleteExpiredOAuthTokens(ctx)
	return n, internal(err)
}

// clip bounds client-supplied values before they are logged.
func clip(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
