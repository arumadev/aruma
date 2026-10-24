package mgmt

import "github.com/arumadev/aruma/internal/platform/httpx"

// rulesRoutes registers the firebaserules v1 API (C §1.5, R1 to R8) under /v1/projects/: :test,
// rulesets and releases, over tenancy, with Options.RulesLimits and Options.Inflight.
//
// S5-A registers the routes.
func (a *API) rulesRoutes(r *httpx.Router) {}
