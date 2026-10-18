package core

import (
	"context"

	"github.com/osama300/aurma-project/internal/tenancy/queries"
)

// ProjectProfile is what Auth's emails and action links show of a project.
type ProjectProfile struct {
	ID          string
	DisplayName string
	AuthDomain  string
	// WebAPIKey is the key the action links carry, as Firebase's links carry the web key: the
	// active key of the web platform, else the oldest active key, else empty.
	WebAPIKey string
}

// ProjectProfile returns the profile of an active project by number.
func (s *Service) ProjectProfile(ctx context.Context, number int64) (ProjectProfile, error) {
	p, err := s.q.GetProject(ctx, number)
	if err != nil || p.State != "ACTIVE" {
		return ProjectProfile{}, errNotFound()
	}
	out := ProjectProfile{ID: p.ProjectID, DisplayName: p.DisplayName, AuthDomain: p.AuthDomain}
	web := "WEB"
	if k, err := s.q.GetPlatformAPIKey(ctx, queries.GetPlatformAPIKeyParams{ProjectNumber: number, Platform: &web}); err == nil && k.State == "ACTIVE" {
		out.WebAPIKey = k.KeyString
		return out, nil
	} else if err != nil && !notFound(err) {
		return out, internal(err)
	}
	keys, err := s.q.ListAPIKeys(ctx, number)
	if err != nil {
		return out, internal(err)
	}
	for _, k := range keys {
		if k.State == "ACTIVE" {
			out.WebAPIKey = k.KeyString
			break
		}
	}
	return out, nil
}
