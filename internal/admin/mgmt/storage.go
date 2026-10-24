package mgmt

import "github.com/arumadev/aruma/internal/platform/httpx"

// storageRoutes registers what the firebase CLI asks of Storage besides the rules (ADR-022 §15):
// GET /v1alpha/projects/{p}/defaultBucket, GET /v1beta/projects/{p}/buckets, and Service
// Usage's GET /v1/projects/{p}/services/{name} and :enable.
//
// S5-A registers the routes.
func (a *API) storageRoutes(r *httpx.Router) {}
