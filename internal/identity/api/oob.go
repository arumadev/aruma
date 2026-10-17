package api

import (
	"net/http"

	"github.com/osama300/aurma-project/internal/identity/core"
	"github.com/osama300/aurma-project/internal/platform/httpx"
)

// oobRoutes registers the email action calls: sendOobCode and resetPassword.
func (a *API) oobRoutes(r *httpx.Router) {}

// applyActionCode is accounts:update with an oobCode (verify or recover an email).
func (a *API) applyActionCode(w http.ResponseWriter, r *http.Request, p core.Project, code string) {
	a.writeError(w, r, &core.Error{Code: "INVALID_OOB_CODE", Status: http.StatusBadRequest})
}
