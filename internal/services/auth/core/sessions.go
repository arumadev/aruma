package core

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/osama300/aurma-project/internal/platform/crypto"
	"github.com/osama300/aurma-project/internal/services/auth/queries"
	"github.com/osama300/aurma-project/internal/services/auth/tokens"
)

// TokenLifetime is the ID token lifetime (ADR-004).
const TokenLifetime = time.Hour

// recentLogin is how fresh auth_time must be for sensitive changes (email, password, delete).
const recentLogin = 5 * time.Minute

// Session is the result of a sign-in or a refresh.
type Session struct {
	IDToken      string
	RefreshToken string
	ExpiresIn    int
	User         User
	IsNewUser    bool
}

// IDClaims are the parsed claims of an ID token.
type IDClaims struct {
	Iss      string `json:"iss"`
	Aud      string `json:"aud"`
	Sub      string `json:"sub"`
	Iat      int64  `json:"iat"`
	Exp      int64  `json:"exp"`
	AuthTime int64  `json:"auth_time"`
	Firebase struct {
		SignInProvider string `json:"sign_in_provider"`
	} `json:"firebase"`
}

// mint signs an ID token for the user. Developer claims (from the user record and, for custom
// tokens, from the token) sit at the top level as in Firebase; they never override the standard
// claims, which validClaims already forbids. It is never issued before its sign-in: a session
// started by a revocation is dated at the revocation point, up to a second ahead.
func (s *Service) mint(ctx context.Context, p Project, u User, provider string, tokenClaims json.RawMessage, authTime time.Time) (string, error) {
	now := s.now()
	if authTime.After(now) {
		now = authTime
	}
	claims := map[string]any{}
	for _, raw := range []json.RawMessage{u.CustomClaims, tokenClaims} {
		if len(raw) == 0 {
			continue
		}
		var m map[string]any
		if json.Unmarshal(raw, &m) == nil {
			for k, v := range m {
				if !reservedClaims[k] {
					claims[k] = v
				}
			}
		}
	}
	identities := map[string][]string{}
	for _, pr := range u.Providers {
		if pr.ProviderID != "password" {
			identities[pr.ProviderID] = append(identities[pr.ProviderID], pr.RawID)
		}
	}
	if u.Email != "" {
		identities["email"] = []string{u.Email}
		claims["email"] = u.Email
		claims["email_verified"] = u.EmailVerified
	}
	if u.DisplayName != "" {
		claims["name"] = u.DisplayName
	}
	if u.PhotoURL != "" {
		claims["picture"] = u.PhotoURL
	}
	if provider == "anonymous" {
		claims["provider_id"] = "anonymous"
	}
	firebase := map[string]any{"identities": identities, "sign_in_provider": provider}
	if p.Tenant != "" {
		firebase["tenant"] = p.Tenant
	}
	claims["iss"] = s.Issuer(p)
	claims["aud"] = p.ID
	claims["sub"] = u.UID
	claims["user_id"] = u.UID
	claims["iat"] = now.Unix()
	claims["exp"] = now.Add(TokenLifetime).Unix()
	claims["auth_time"] = authTime.Unix()
	claims["firebase"] = firebase
	return s.tokens.Sign(ctx, p.Number, tokens.IDToken, claims)
}

// startSession mints an ID token and a new refresh token for a sign-in.
func (s *Service) startSession(ctx context.Context, q *queries.Queries, p Project, u User, provider string, tokenClaims json.RawMessage, isNew bool) (*Session, error) {
	refresh := crypto.RandomString(crypto.TokenBytes)
	authTime, err := q.CreateRefreshToken(ctx, queries.CreateRefreshTokenParams{
		TokenHash: s.keys.HashToken("auth-refresh:" + refresh), ProjectNumber: p.Number, TenantID: p.Tenant,
		Uid: u.UID, SignInProvider: provider, CustomClaims: tokenClaims,
	})
	if err != nil {
		return nil, err
	}
	if err := q.TouchLogin(ctx, queries.TouchLoginParams{ProjectNumber: p.Number, TenantID: p.Tenant, Uid: u.UID}); err != nil {
		return nil, err
	}
	idToken, err := s.mint(ctx, p, u, provider, tokenClaims, authTime)
	if err != nil {
		return nil, err
	}
	return &Session{IDToken: idToken, RefreshToken: refresh, ExpiresIn: int(TokenLifetime.Seconds()), User: u, IsNewUser: isNew}, nil
}

// Refresh exchanges a refresh token for a new ID token. The refresh token is returned as is,
// so browser tabs refreshing at once do not sign each other out. Revoked, deleted and disabled
// sessions answer TOKEN_EXPIRED, USER_NOT_FOUND and USER_DISABLED: the codes that make the
// client SDKs sign the user out.
func (s *Service) Refresh(ctx context.Context, p Project, refresh string) (*Session, error) {
	if refresh == "" {
		return nil, ErrMissingRefreshToken
	}
	hash := s.keys.HashToken("auth-refresh:" + refresh)
	row, err := s.q.GetRefreshToken(ctx, queries.GetRefreshTokenParams{ProjectNumber: p.Number, TokenHash: hash})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrInvalidRefreshToken
		}
		return nil, err
	}
	p.Tenant = row.TenantID
	raw, err := s.q.GetUser(ctx, queries.GetUserParams{ProjectNumber: p.Number, TenantID: row.TenantID, Uid: row.Uid})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		_ = s.q.DeleteRefreshToken(ctx, queries.DeleteRefreshTokenParams{ProjectNumber: p.Number, TokenHash: hash})
		return nil, ErrUserNotFound
	case err != nil:
		return nil, err
	case row.CreatedAt.Before(raw.CreatedAt):
		// The uid was deleted and reused: this token belongs to the earlier user.
		return nil, ErrUserNotFound
	case raw.Disabled:
		return nil, ErrUserDisabled
	case row.CreatedAt.Unix() < raw.TokensValidAfter.Unix():
		// Second precision, as for ID tokens and Firebase's validSince. The revocation point is
		// a whole second after the revocation, so this ends every earlier session, while the
		// session a password change returns is dated at that point and differs from the one the
		// web SDK holds, which makes the SDK take it.
		_ = s.q.DeleteRefreshToken(ctx, queries.DeleteRefreshTokenParams{ProjectNumber: p.Number, TokenHash: hash})
		return nil, ErrTokenExpired
	}
	u, err := s.loadUser(ctx, s.q, p, raw)
	if err != nil {
		return nil, err
	}
	idToken, err := s.mint(ctx, p, u, row.SignInProvider, row.CustomClaims, row.AuthTime)
	if err != nil {
		return nil, err
	}
	_ = s.q.TouchRefreshToken(ctx, queries.TouchRefreshTokenParams{ProjectNumber: p.Number, TokenHash: hash})
	_ = s.q.TouchRefresh(ctx, queries.TouchRefreshParams{ProjectNumber: p.Number, TenantID: p.Tenant, Uid: u.UID})
	return &Session{IDToken: idToken, RefreshToken: refresh, ExpiresIn: int(TokenLifetime.Seconds()), User: u}, nil
}

// VerifyIDToken checks an ID token of the project and returns its live user. Malformed,
// foreign or expired tokens are INVALID_ID_TOKEN; tokens issued before a revocation are
// TOKEN_EXPIRED; deleted and disabled users answer USER_NOT_FOUND and USER_DISABLED.
func (s *Service) VerifyIDToken(ctx context.Context, p Project, idToken string) (User, IDClaims, error) {
	var c IDClaims
	payload, err := s.tokens.Verify(ctx, p.Number, tokens.IDToken, idToken)
	if err != nil {
		return User{}, c, ErrInvalidIDToken
	}
	if json.Unmarshal(payload, &c) != nil || c.Iss != s.Issuer(p) || c.Aud != p.ID || c.Sub == "" {
		return User{}, c, ErrInvalidIDToken
	}
	now := s.now().Unix()
	if c.Exp <= now || c.Iat > now+300 {
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
	if c.Iat < raw.TokensValidAfter.Unix() || raw.CreatedAt.Unix() > c.Iat {
		return User{}, c, ErrTokenExpired
	}
	u, err := s.loadUser(ctx, s.q, p, raw)
	return u, c, err
}

// requireRecent refuses sensitive changes on a sign-in older than recentLogin.
func (s *Service) requireRecent(c IDClaims) error {
	if s.now().Sub(time.Unix(c.AuthTime, 0)) > recentLogin {
		return ErrCredentialTooOld
	}
	return nil
}
