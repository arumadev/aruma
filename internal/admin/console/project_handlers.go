package console

import (
	"net/http"

	"github.com/osama300/aurma-project/internal/platform/authn"
)

// projectRoutes registers the project, app, key and service account routes.
func (a *API) projectRoutes(with func(authn.Policy, string, string, http.HandlerFunc)) {}
