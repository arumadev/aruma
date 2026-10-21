package core

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/arumadev/aruma/internal/platform/apierr"
	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/platform/crypto"
	"github.com/arumadev/aruma/internal/tenancy/queries"
)

// touchEvery limits how often last-seen timestamps are written.
const touchEvery = time.Minute

var errInvalidCredential = apierr.New(apierr.Unauthenticated,
	"Request had invalid authentication credentials.")

// VerifySession implements authn.Verifier. Valid sessions slide their idle expiry forward,
// never past the absolute expiry.
func (s *Service) VerifySession(ctx context.Context, token string) (*authn.Principal, error) {
	row, err := s.q.GetActiveSession(ctx, s.keys.HashToken(token))
	if err != nil {
		if notFound(err) {
			return nil, errInvalidCredential
		}
		return nil, internal(err)
	}
	if row.AccountDisabled {
		return nil, errInvalidCredential
	}
	sess := row.ControlSession
	if time.Since(sess.LastSeenAt) > touchEvery {
		_ = s.q.TouchSession(ctx, queries.TouchSessionParams{ID: sess.ID, IdleExpiresAt: time.Now().Add(s.opts.IdleTimeout)})
	}
	return &authn.Principal{Kind: authn.KindAccount, Method: authn.MethodSession, AccountID: sess.AccountID, SessionID: sess.ID}, nil
}

// VerifyAccessToken implements authn.Verifier for personal CLI tokens.
func (s *Service) VerifyAccessToken(ctx context.Context, token string) (*authn.Principal, error) {
	row, err := s.q.GetActiveAccessToken(ctx, s.keys.HashToken(token))
	if err != nil {
		if notFound(err) {
			return nil, errInvalidCredential
		}
		return nil, internal(err)
	}
	if row.AccountDisabled {
		return nil, errInvalidCredential
	}
	t := row.ControlAccessToken
	if t.LastUsedAt == nil || time.Since(*t.LastUsedAt) > touchEvery {
		_ = s.q.TouchAccessToken(ctx, t.ID)
	}
	p := &authn.Principal{Kind: authn.KindAccount, Method: authn.MethodAccessToken, AccountID: t.AccountID}
	if t.ExpiresAt != nil {
		p.ExpiresAt = *t.ExpiresAt
	}
	return p, nil
}

// Session is an active console session as listed to its owner.
type Session struct {
	ID         uuid.UUID
	Current    bool
	IP         string
	UserAgent  string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
}

// ListSessions returns the caller's active sessions.
func (s *Service) ListSessions(ctx context.Context, p *authn.Principal) ([]Session, error) {
	rows, err := s.q.ListSessions(ctx, p.AccountID)
	if err != nil {
		return nil, internal(err)
	}
	out := make([]Session, 0, len(rows))
	for _, r := range rows {
		sess := Session{ID: r.ID, Current: r.ID == p.SessionID, CreatedAt: r.CreatedAt, LastSeenAt: r.LastSeenAt, ExpiresAt: r.IdleExpiresAt}
		if r.Ip != nil {
			sess.IP = r.Ip.String()
		}
		if r.UserAgent != nil {
			sess.UserAgent = *r.UserAgent
		}
		out = append(out, sess)
	}
	return out, nil
}

// RevokeSession signs out one of the caller's sessions.
func (s *Service) RevokeSession(ctx context.Context, p *authn.Principal, id uuid.UUID) error {
	n, err := s.q.RevokeSession(ctx, queries.RevokeSessionParams{ID: id, AccountID: p.AccountID})
	if err != nil {
		return internal(err)
	}
	if n == 0 {
		return errNotFound()
	}
	return nil
}

// AccessToken is a personal CLI token as listed to its owner. Plaintext is set only on the
// response that creates it.
type AccessToken struct {
	ID         uuid.UUID
	Name       string
	Prefix     string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	ExpiresAt  *time.Time
	Plaintext  string
}

func toAccessToken(t queries.ControlAccessToken) AccessToken {
	return AccessToken{ID: t.ID, Name: t.Name, Prefix: t.Prefix, CreatedAt: t.CreatedAt, LastUsedAt: t.LastUsedAt, ExpiresAt: t.ExpiresAt}
}

// maxTokenLifetime caps CLI token expiry; zero days means no expiry.
const maxTokenLifetime = 366 * 24 * time.Hour

// CreateAccessToken issues a personal CLI token. Its plaintext is returned once.
func (s *Service) CreateAccessToken(ctx context.Context, p *authn.Principal, name string, lifetime time.Duration) (AccessToken, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 100 {
		return AccessToken{}, apierr.Invalid("name", "Token name must be 1 to 100 characters.")
	}
	if lifetime < 0 || lifetime > maxTokenLifetime {
		return AccessToken{}, apierr.Invalid("expireDays", "Token lifetime must be between 1 and 366 days, or 0 for no expiry.")
	}
	plain := crypto.NewToken(authn.AccessTokenPrefix)
	var expires *time.Time
	if lifetime > 0 {
		e := time.Now().Add(lifetime)
		expires = &e
	}
	t, err := s.q.CreateAccessToken(ctx, queries.CreateAccessTokenParams{
		AccountID: p.AccountID,
		Name:      name,
		Prefix:    plain[:len(authn.AccessTokenPrefix)+6],
		TokenHash: s.keys.HashToken(plain),
		ExpiresAt: expires,
	})
	if err != nil {
		return AccessToken{}, internal(err)
	}
	out := toAccessToken(t)
	out.Plaintext = plain
	return out, nil
}

// ListAccessTokens returns the caller's unrevoked CLI tokens.
func (s *Service) ListAccessTokens(ctx context.Context, p *authn.Principal) ([]AccessToken, error) {
	rows, err := s.q.ListAccessTokens(ctx, p.AccountID)
	if err != nil {
		return nil, internal(err)
	}
	out := make([]AccessToken, 0, len(rows))
	for _, r := range rows {
		out = append(out, toAccessToken(r))
	}
	return out, nil
}

// RevokeAccessToken revokes one of the caller's CLI tokens; it stops working at once (AC-7).
func (s *Service) RevokeAccessToken(ctx context.Context, p *authn.Principal, id uuid.UUID) error {
	n, err := s.q.RevokeAccessToken(ctx, queries.RevokeAccessTokenParams{ID: id, AccountID: p.AccountID})
	if err != nil {
		return internal(err)
	}
	if n == 0 {
		return errNotFound()
	}
	return nil
}

// VerifyServiceToken implements authn.Verifier for service account access tokens.
func (s *Service) VerifyServiceToken(ctx context.Context, token string) (*authn.Principal, error) {
	return s.verifyServiceToken(ctx, token)
}
