// Package console serves the dashboard's Storage screens under
// /console/v1/projects/{project}/storage (ADR-022 §15): browsing by prefix, upload sessions the
// dashboard issues (whose chunks go to /v0, without the cookie), downloads through media in its
// Console mode, deletion, metadata, tokens, CORS, usage, and the Storage rules through tenancy.
// Every write is audited.
package console

import (
	"github.com/arumadev/aruma/internal/platform/consolemount"
	"github.com/arumadev/aruma/internal/services/storage/core"
)

// Routes returns the mount that registers the Storage screens' routes with the console API.
// svc is nil while Storage is disabled.
//
// S5-B registers the routes, with their entries in internal/app/testdata/console_routes.txt.
func Routes(svc *core.Service) func(*consolemount.Mount) {
	return func(m *consolemount.Mount) {}
}
