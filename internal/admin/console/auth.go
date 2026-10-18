package console

import (
	"errors"
	"net/http"

	identity "github.com/osama300/aurma-project/internal/identity/core"
	"github.com/osama300/aurma-project/internal/platform/apierr"
)

// The project Auth screens act through the Auth service on a project the caller owns. Their
// routes live in auth_*.go files, one per area.

// authProject resolves the {project} of the request for the signed-in owner.
func (a *API) authProject(r *http.Request) (identity.Project, error) {
	p, err := a.svc.GetProject(r.Context(), principal(r), r.PathValue("project"))
	if err != nil {
		return identity.Project{}, err
	}
	return identity.Project{Number: p.Number, ID: p.ID}, nil
}

// authError renders Auth errors as google.rpc.Status, like the rest of the console API. The
// Identity Toolkit code stays in the message so the dashboard can map it.
func authError(err error) error {
	var e *identity.Error
	if !errors.As(err, &e) {
		return err
	}
	code := apierr.InvalidArgument
	switch e.Code {
	case identity.ErrUserNotFound.Code:
		code = apierr.NotFound
	case identity.ErrEmailExists.Code, identity.ErrUIDExists.Code:
		code = apierr.AlreadyExists
	}
	return apierr.New(code, "%s", e.Error())
}

func (a *API) failAuth(w http.ResponseWriter, r *http.Request, err error) {
	a.fail(w, r, authError(err))
}
