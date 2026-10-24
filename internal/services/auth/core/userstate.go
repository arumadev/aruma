package core

import (
	"context"

	"github.com/arumadev/aruma/internal/platform/idtoken"
)

// Users reports users' live state for a service that acts for a user long after it verified the
// token: Storage, when an upload started days ago finalizes (ADR-022 §7). internal/app hands it
// over, as it does IDTokenVerifier.
func (s *Service) Users() idtoken.Users { return userStates{s} }

type userStates struct{ s *Service }

// State implements idtoken.Users from the revocation state the Verifier caches.
func (u userStates) State(ctx context.Context, project int64, uid string) (idtoken.UserState, error) {
	panic("S2-D: Users.State")
}
