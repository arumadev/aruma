// Package console serves the dashboard's project Auth screens under
// /console/v1/projects/{project}/auth. The console API mounts these routes and authenticates
// them; they act through the Auth service on a project the caller owns. The routes live in one
// file per area: users.go, email.go, providers.go.
package console

import (
	"errors"
	"net/http"

	"github.com/arumadev/aruma/internal/platform/apierr"
	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/platform/consolemount"
	"github.com/arumadev/aruma/internal/services/auth/core"
)

// anyCred accepts the owner's session or a CLI token, like the console's other project routes.
var anyCred = authn.Policy{Session: true, AccessToken: true}

// Routes returns the mount that registers the Auth screens' routes with the console API.
func Routes(svc *core.Service) func(*consolemount.Mount) {
	return func(m *consolemount.Mount) {
		h := &handlers{svc: svc, m: m}
		h.authRoutes()
		h.authEmailRoutes()
		h.authProviderRoutes()
	}
}

type handlers struct {
	svc *core.Service
	m   *consolemount.Mount
}

// project resolves the {project} of the request for the signed-in owner.
func (h *handlers) project(r *http.Request) (core.Project, error) {
	p, err := h.m.Project(r)
	if err != nil {
		return core.Project{}, err
	}
	return core.Project{Number: p.Number, ID: p.ID}, nil
}

// authError renders Auth errors as google.rpc.Status, like the rest of the console API. The
// Identity Toolkit code stays in the message so the dashboard can map it.
func authError(err error) error {
	var e *core.Error
	if !errors.As(err, &e) {
		return err
	}
	code := apierr.InvalidArgument
	switch e.Code {
	case core.ErrUserNotFound.Code:
		code = apierr.NotFound
	case core.ErrEmailExists.Code, core.ErrUIDExists.Code:
		code = apierr.AlreadyExists
	}
	return apierr.New(code, "%s", e.Error())
}

func (h *handlers) failAuth(w http.ResponseWriter, r *http.Request, err error) {
	h.m.Fail(w, r, authError(err))
}
