package api

import (
	"net/http"

	"github.com/osama300/aurma-project/internal/identity/core"
	"github.com/osama300/aurma-project/internal/platform/httpx"
)

// idpRoutes registers sign-in with Google and Apple.
func (a *API) idpRoutes(r *httpx.Router) {}

// idpAuthURI is accounts:createAuthUri for an identity provider.
func (a *API) idpAuthURI(w http.ResponseWriter, r *http.Request, p core.Project) {
	a.writeError(w, r, core.ErrOperationNotAllowed)
}
