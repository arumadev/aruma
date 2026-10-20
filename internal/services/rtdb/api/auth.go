package api

import (
	"context"
	"errors"
	"strings"

	"github.com/osama300/aurma-project/internal/platform/authn"
	"github.com/osama300/aurma-project/internal/services/rtdb/core"
	"github.com/osama300/aurma-project/internal/services/rtdb/wire"
)

// authenticate handles auth and gauth. Until R4 only admin credentials mean anything: a
// service account's access token for this project (what the Admin SDKs send, from Aruma's
// OAuth endpoint), or a personal CLI token of the project's owner. Both bypass rules, as
// admin credentials do in Firebase. Any other credential, such as a user's ID token, signs
// the session out and is answered ok, so web and mobile clients keep their connection;
// the locked rules then deny them everything (ADR-020).
func (s *session) authenticate(ctx context.Context, b wire.AuthBody) wire.Reply {
	token := strings.TrimSpace(b.Credential)
	if !strings.HasPrefix(token, authn.ServiceTokenPrefix) && !strings.HasPrefix(token, authn.AccessTokenPrefix) {
		s.core.SetAuth(core.Auth{})
		return wire.OKWith(map[string]any{"auth": nil, "expires": nil})
	}
	p, err := s.api.verifyAdmin(ctx, s.target, token)
	if err != nil {
		s.core.SetAuth(core.Auth{})
		s.api.log.Info("rtdb: admin credential refused", "database", s.target.Name, "error", err)
		return wire.Fail(wire.StatusInvalidToken, "Invalid token.")
	}
	auth := core.Auth{Admin: true, Account: p.Email}
	if b.NoAuth {
		// An Admin SDK asking to act as an unauthenticated client.
		auth = core.Auth{}
	}
	s.core.SetAuth(auth)
	return wire.OKWith(map[string]any{"auth": nil, "expires": s.api.svc.Now().Add(authExpiry).Unix()})
}

// verifyAdmin checks an admin credential and that it may act on the target's project: a
// service account of that project, or an account that owns it.
func (a *API) verifyAdmin(ctx context.Context, t core.Target, token string) (*authn.Principal, error) {
	if a.opts.Credentials == nil || a.opts.Projects == nil {
		return nil, errors.New("admin credentials are not configured")
	}
	var (
		p   *authn.Principal
		err error
	)
	if strings.HasPrefix(token, authn.ServiceTokenPrefix) {
		p, err = a.opts.Credentials.VerifyServiceToken(ctx, token)
	} else {
		p, err = a.opts.Credentials.VerifyAccessToken(ctx, token)
	}
	if err != nil {
		return nil, err
	}
	if _, err := a.opts.Projects.GetProject(ctx, p, t.ProjectID); err != nil {
		return nil, err
	}
	return p, nil
}
