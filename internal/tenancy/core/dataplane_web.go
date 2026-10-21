package core

import (
	"context"
	"strings"

	"github.com/arumadev/aruma/internal/tenancy/queries"
)

// Calls for the Auth pages served on a project's authDomain (/__/auth/*, /__/firebase/init.json).

// ProjectAuthDomain returns the authDomain of an active project.
func (s *Service) ProjectAuthDomain(ctx context.Context, number int64) (string, error) {
	p, err := s.q.GetProject(ctx, number)
	if err != nil || p.State != "ACTIVE" {
		return "", errNotFound()
	}
	return p.AuthDomain, nil
}

// WebAppForAuthDomain returns the one active project whose own authDomain is host, with its
// first active web app. The instance's shared project domain, every project's default, names
// none, even while only one project uses it.
func (s *Service) WebAppForAuthDomain(ctx context.Context, host string) (AppConfig, error) {
	host = strings.ToLower(host)
	if host == strings.ToLower(s.opts.ProjectDomain) {
		return AppConfig{}, errNotFound()
	}
	rows, err := s.q.ListActiveProjectsByAuthDomain(ctx, host)
	if err != nil {
		return AppConfig{}, internal(err)
	}
	if len(rows) != 1 {
		return AppConfig{}, errNotFound()
	}
	proj := rows[0]
	apps, err := s.q.ListActiveAppsByPlatform(ctx, queries.ListActiveAppsByPlatformParams{ProjectNumber: proj.ProjectNumber, Platform: PlatformWeb})
	if err != nil {
		return AppConfig{}, internal(err)
	}
	if len(apps) == 0 {
		return AppConfig{}, errNotFound()
	}
	key, err := s.appKey(ctx, s.q, apps[0])
	if err != nil {
		return AppConfig{}, err
	}
	app := KeyedApp{toApp(apps[0], proj.ProjectID), key}
	return AppConfig{Project: s.toProject(proj), App: app, Apps: []KeyedApp{app}}, nil
}
