// Package console serves the dashboard's Storage screens under
// /console/v1/projects/{project}/storage (ADR-022 §15): browsing by prefix, upload sessions the
// dashboard issues (whose chunks go to /v0, without the cookie), downloads through media in its
// Console mode, deletion, metadata, tokens, CORS, usage, and the Storage rules through tenancy.
// Every write is audited.
package console

import (
	"context"

	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/platform/consolemount"
	"github.com/arumadev/aruma/internal/platform/rules"
	"github.com/arumadev/aruma/internal/services/storage/core"
	tcore "github.com/arumadev/aruma/internal/tenancy/core"
)

// Rules is the control plane's rulesets and releases, as the rules editor needs them; tenancy
// implements it.
type Rules interface {
	CreateRuleset(ctx context.Context, p *authn.Principal, project string, svc rules.Service, files []rules.File) (tcore.Ruleset, rules.Issues, error)
	GetRuleset(ctx context.Context, p *authn.Principal, project, name string) (tcore.Ruleset, error)
	GetRelease(ctx context.Context, p *authn.Principal, project, release string) (tcore.Release, error)
	PublishRelease(ctx context.Context, p *authn.Principal, project, release, ruleset string) (tcore.Release, error)
}

// Options configure the routes.
type Options struct {
	// PublicURL is the server's address without a trailing slash: the /v0 session URLs the
	// dashboard's uploads go to are absolute from it.
	PublicURL string
	// ProjectDomain names a project's bucket, <project_id>.<project_domain> (ADR-022 §2).
	ProjectDomain string
	// Rules saves, reads and publishes the Storage rules.
	Rules Rules
	// Audit records the dashboard's writes.
	Audit func(ctx context.Context, p *authn.Principal, project int64, action, target string, details map[string]any) error
}

// Routes returns the mount that registers the Storage screens' routes with the console API.
// svc is nil while Storage is disabled.
//
// S5-B registers the routes, with their entries in internal/app/testdata/console_routes.txt.
func Routes(svc *core.Service, opts Options) func(*consolemount.Mount) {
	return func(m *consolemount.Mount) {}
}
