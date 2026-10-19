package core

import (
	"context"
	"errors"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/osama300/aurma-project/internal/services/auth/queries"
)

// Apple tokens are revoked at Apple when the app asks (accounts:revokeToken, which the web
// SDK's revokeAccessToken calls) and when the account is deleted: App Store review requires
// that deleting an account also revokes its Sign in with Apple tokens.

// RevokeInput is v2 accounts:revokeToken.
type RevokeInput struct {
	ProviderID string
	TokenType  string // ACCESS_TOKEN or REFRESH_TOKEN
	Token      string
	IDToken    string
}

// RevokeToken revokes an Apple token of the signed-in user, who must have Apple linked.
func (s *Service) RevokeToken(ctx context.Context, p Project, in RevokeInput) error {
	u, _, err := s.VerifyIDToken(ctx, p, in.IDToken)
	if err != nil {
		return err
	}
	if in.ProviderID != AppleProvider {
		return failf("INVALID_PROVIDER_ID", "only apple.com tokens can be revoked")
	}
	hint := map[string]string{"ACCESS_TOKEN": "access_token", "REFRESH_TOKEN": "refresh_token"}[in.TokenType]
	if hint == "" {
		return failf("INVALID_REQ_TYPE", "tokenType must be ACCESS_TOKEN or REFRESH_TOKEN")
	}
	if in.Token == "" {
		return failf("MISSING_TOKEN", "")
	}
	linked := false
	for _, pr := range u.Providers {
		linked = linked || pr.ProviderID == AppleProvider
	}
	if !linked {
		return failf("INVALID_IDP_RESPONSE", "the user has no Apple account linked")
	}
	idp, err := s.enabledIdP(ctx, p, AppleProvider)
	if err != nil {
		return err
	}
	if err := s.revokeAtApple(ctx, idp, in.Token, hint); err != nil {
		s.log.Info("apple revoke", "error", err)
		return errIdPToken("Apple did not accept the token")
	}
	s.audit(ctx, s.q, p, u.UID, "idp.revoke", "ok", map[string]any{"provider": AppleProvider, "tokenType": in.TokenType})
	return nil
}

func (s *Service) revokeAtApple(ctx context.Context, idp *idpCreds, token, hint string) error {
	secret, err := s.appleClientSecret(idp)
	if err != nil {
		return err
	}
	form := url.Values{"client_id": {idp.clientID}, "client_secret": {secret}, "token": {token}, "token_type_hint": {hint}}
	return idp.post(ctx, idp.endpoints.RevokeURL, form, nil)
}

// appleRefreshToken returns the Apple refresh token kept for a user, or "".
func (s *Service) appleRefreshToken(ctx context.Context, q *queries.Queries, p Project, uid string) string {
	row, err := q.GetIdPToken(ctx, queries.GetIdPTokenParams{ProjectNumber: p.Number, TenantID: p.Tenant, Uid: uid, ProviderID: AppleProvider})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			s.log.Warn("apple token", "error", err)
		}
		return ""
	}
	b, err := s.keys.Open(row.RefreshTokenEnc, idpTokenAAD(p, uid, AppleProvider))
	if err != nil {
		s.log.Warn("apple token", "error", err)
		return ""
	}
	return string(b)
}

// revokeAppleOnDelete revokes a deleted user's Apple refresh token. The account is already
// gone, so a failure is logged and recorded, not returned.
func (s *Service) revokeAppleOnDelete(ctx context.Context, p Project, uid, token string) {
	if token == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	result := "ok"
	idp, err := s.loadIdP(ctx, p, AppleProvider, false)
	if err == nil {
		err = s.revokeAtApple(ctx, idp, token, "refresh_token")
	}
	if err != nil {
		result = "error"
		s.log.Warn("apple revoke on delete", "error", err)
	}
	s.audit(ctx, s.q, p, uid, "idp.revoke", result, map[string]any{"provider": AppleProvider, "onDelete": true})
}
