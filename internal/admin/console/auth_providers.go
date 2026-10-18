package console

import (
	"net/http"

	"github.com/osama300/aurma-project/internal/platform/authn"
)

// authProviderRoutes registers the Google and Apple sign-in settings routes.
func (a *API) authProviderRoutes(with func(authn.Policy, string, string, http.HandlerFunc)) {}
