package console

import (
	"net/http"

	"github.com/osama300/aurma-project/internal/platform/authn"
)

// authRoutes registers the Users, Sign-in methods and Settings routes.
func (a *API) authRoutes(with func(authn.Policy, string, string, http.HandlerFunc)) {}
