package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/osama300/aurma-project/internal/identity/queries"
	"github.com/osama300/aurma-project/internal/identity/tokens"
)

// Session cookies (createSessionCookie) carry the claims of the ID token they were made from,
// under the project's session issuer and keys (ADR-004), so neither token passes for the other.

// Session cookie lifetimes Firebase allows.
const (
	MinCookieDuration = 5 * time.Minute
	MaxCookieDuration = 14 * 24 * time.Hour
)

// ErrInvalidDuration is a session cookie lifetime outside 5 minutes to 14 days.
var ErrInvalidDuration = fail("INVALID_DURATION")

// CreateSessionCookie exchanges a valid ID token for a session cookie that lives validFor.
// The ID token must verify as VerifyIDToken requires: current, of this project, its user live
// and enabled and its session not revoked. How recent the sign-in must be is the developer's
// choice (auth_time is in the cookie), as Firebase documents.
func (s *Service) CreateSessionCookie(ctx context.Context, p Project, idToken string, validFor time.Duration) (string, error) {
	if validFor < MinCookieDuration || validFor > MaxCookieDuration {
		return "", ErrInvalidDuration
	}
	if idToken == "" {
		return "", ErrInvalidIDToken
	}
	u, _, err := s.VerifyIDToken(ctx, p, idToken)
	if err != nil {
		return "", err
	}
	// VerifyIDToken checked the signature, so the payload is the one Aruma issued.
	claims := map[string]any{}
	_, payload, _ := strings.Cut(idToken, ".")
	payload, _, _ = strings.Cut(payload, ".")
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil || json.Unmarshal(raw, &claims) != nil {
		return "", ErrInvalidIDToken
	}
	now := s.now()
	claims["iss"] = s.SessionIssuer(p)
	claims["iat"] = now.Unix()
	claims["exp"] = now.Add(validFor).Unix()
	cookie, err := s.tokens.Sign(ctx, p.Number, tokens.SessionCookie, claims)
	if err == nil {
		s.audit(ctx, s.q, p, u.UID, "admin.session_cookie.create", "ok", map[string]any{"valid_seconds": int64(validFor.Seconds())})
	}
	return cookie, err
}

// VerifySessionCookie checks a session cookie of the project and returns its live user, as
// VerifyIDToken does for ID tokens. A cookie made before the user's sessions were revoked is
// TOKEN_EXPIRED; anything malformed, foreign or expired is INVALID_ID_TOKEN.
func (s *Service) VerifySessionCookie(ctx context.Context, p Project, cookie string) (User, IDClaims, error) {
	var c IDClaims
	payload, err := s.tokens.Verify(ctx, p.Number, tokens.SessionCookie, cookie)
	if err != nil {
		return User{}, c, ErrInvalidIDToken
	}
	if json.Unmarshal(payload, &c) != nil || c.Iss != s.SessionIssuer(p) || c.Aud != p.ID || !validUID(c.Sub) {
		return User{}, c, ErrInvalidIDToken
	}
	now := s.now().Unix()
	if c.Exp <= now || c.Iat > now+300 || c.Exp-c.Iat > int64(MaxCookieDuration.Seconds()) {
		return User{}, c, ErrInvalidIDToken
	}
	raw, err := s.q.GetUser(ctx, queries.GetUserParams{ProjectNumber: p.Number, TenantID: p.Tenant, Uid: c.Sub})
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, c, ErrUserNotFound
	}
	if err != nil {
		return User{}, c, err
	}
	if raw.Disabled {
		return User{}, c, ErrUserDisabled
	}
	if c.AuthTime < raw.TokensValidAfter.Unix() || raw.CreatedAt.Unix() > c.Iat {
		return User{}, c, ErrTokenExpired
	}
	u, err := s.loadUser(ctx, s.q, p, raw)
	return u, c, err
}
