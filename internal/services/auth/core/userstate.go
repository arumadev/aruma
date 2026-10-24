package core

import (
	"context"
	"time"

	"github.com/arumadev/aruma/internal/platform/idtoken"
)

// Users reports users' live state for a service that acts for a user long after it verified the
// token: Storage, when an upload started days ago finalizes (ADR-022 §7). internal/app hands it
// over, as it does IDTokenVerifier.
func (s *Service) Users() idtoken.Users { return userStates{s} }

type userStates struct{ s *Service }

// State implements idtoken.Users from the revocation state the Verifier caches, so a change made
// through this Service shows at once and any other within stateCacheTTL. v0.3 has no tenants: the
// user is the project's own.
func (u userStates) State(ctx context.Context, project int64, uid string) (idtoken.UserState, error) {
	st, err := u.s.states.get(ctx, u.s, project, "", uid)
	if err != nil || !st.exists {
		return idtoken.UserState{}, err
	}
	return idtoken.UserState{Exists: true, Disabled: st.disabled, TokensValidAfter: time.Unix(st.validAfter, 0),
		Created: time.Unix(st.createdAt, 0)}, nil
}
