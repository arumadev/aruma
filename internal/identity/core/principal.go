package core

import (
	"context"

	"github.com/osama300/aurma-project/internal/platform/apierr"
	"github.com/osama300/aurma-project/internal/platform/authn"
)

// ErrNoPermission answers an Admin API call on a project the caller may not act on. It is the
// same whether the project is unknown, deleted or someone else's, so it reveals nothing.
var ErrNoPermission = apierr.New(apierr.PermissionDenied, "The caller does not have permission.")

// ProjectForPrincipal resolves the {project} of an Admin API call, by id or number, for an
// authenticated caller: a service account acts only on its own project, an owner's CLI token on
// the owner's projects. Only active projects qualify.
func (s *Service) ProjectForPrincipal(ctx context.Context, principal *authn.Principal, ref string) (Project, error) {
	if principal == nil || ref == "" {
		return Project{}, ErrNoPermission
	}
	p, err := s.control.GetProject(ctx, principal, ref)
	if err != nil {
		if apierr.CodeOf(err) == apierr.NotFound {
			return Project{}, ErrNoPermission
		}
		return Project{}, err
	}
	if p.State != "ACTIVE" {
		return Project{}, ErrNoPermission
	}
	return Project{Number: p.Number, ID: p.ID}, nil
}
