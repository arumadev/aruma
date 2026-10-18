package console

import (
	"net/http"

	"github.com/osama300/aurma-project/internal/platform/authn"
)

// authEmailRoutes registers the email templates, SMTP and dev mailbox routes.
func (a *API) authEmailRoutes(with func(authn.Policy, string, string, http.HandlerFunc)) {}
