// Package console serves the dashboard's Realtime Database screens under
// /console/v1/projects/{project}/database. The console API mounts these routes and
// authenticates them; they act on the default database of a project the caller owns, as an
// admin that rules do not restrict. The routes live in one file per area: data.go, rules.go.
//
// Reads come from the stored rows a page at a time (core.Service.Shallow), so a large node is
// never loaded whole. Writes go through a session and the database's writer like an SDK's, so
// listeners see them at once.
package console

import (
	"errors"
	"net/http"

	"github.com/arumadev/aruma/internal/platform/apierr"
	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/platform/consolemount"
	"github.com/arumadev/aruma/internal/platform/httpx"
	"github.com/arumadev/aruma/internal/services/rtdb/core"
)

// anyCred accepts the owner's session or a CLI token, like the console's other project routes.
var anyCred = authn.Policy{Session: true, AccessToken: true}

// Options configure the routes.
type Options struct {
	// DatabaseURL returns the databaseURL of a project's default database, as config files
	// carry it (ADR-018).
	DatabaseURL func(projectID string) string
	// DevOpenRules is rtdb.dev_open_rules: clients on the server's machine bypass the rules.
	DevOpenRules bool
}

// Routes returns the mount that registers the Realtime Database screens' routes with the
// console API. svc is nil while the database is disabled: the routes still exist, the info
// route says so, and the others answer FAILED_PRECONDITION.
func Routes(svc *core.Service, opts Options) func(*consolemount.Mount) {
	return func(m *consolemount.Mount) {
		h := &handlers{svc: svc, opts: opts, m: m}
		m.Handle(anyCred, "GET", "/console/v1/projects/{project}/database", h.getDatabase)
		h.dataRoutes()
		h.rulesRoutes()
	}
}

type handlers struct {
	svc  *core.Service
	opts Options
	m    *consolemount.Mount
}

// errDisabled answers the data and rules routes of a server that does not serve the database.
var errDisabled = apierr.New(apierr.FailedPrecondition, "Realtime Database is not enabled on this server (rtdb.enabled).")

// target resolves the {project} of the request for the signed-in owner to its default
// database. It fails, ready for Fail, when the project is not the caller's or the database
// is disabled.
func (h *handlers) target(r *http.Request) (core.Target, error) {
	p, err := h.m.Project(r)
	if err != nil {
		return core.Target{}, err
	}
	if h.svc == nil {
		return core.Target{}, errDisabled
	}
	return core.Target{Name: p.ID + core.DatabaseSuffix, ProjectID: p.ID, Project: p.Number}, nil
}

// rtdbError renders a Realtime Database error as google.rpc.Status, keeping its message,
// which follows Firebase's.
func rtdbError(err error) error {
	var e *core.Error
	if !errors.As(err, &e) {
		return err
	}
	code := apierr.InvalidArgument
	switch e.HTTP {
	case http.StatusNotFound:
		code = apierr.NotFound
	case http.StatusPreconditionFailed:
		code = apierr.Aborted
	case http.StatusTooManyRequests:
		code = apierr.ResourceExhausted
	case http.StatusUnauthorized, http.StatusForbidden:
		code = apierr.PermissionDenied
	case http.StatusServiceUnavailable, http.StatusNotImplemented:
		return apierr.Wrap(err, apierr.Unavailable, "%s", e.Message)
	}
	return apierr.New(code, "%s", e.Message)
}

func (h *handlers) fail(w http.ResponseWriter, r *http.Request, err error) {
	h.m.Fail(w, r, rtdbError(err))
}

// infoRows caps the rows the info route counts: beyond it the size is shown as "at least".
const infoRows = 100_000

// getDatabase describes the project's database: its URL, whether the server serves it, its
// size and the limits a value the dashboard writes must respect.
func (h *handlers) getDatabase(w http.ResponseWriter, r *http.Request) {
	p, err := h.m.Project(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	out := map[string]any{
		"name":        p.ID + core.DatabaseSuffix,
		"databaseURL": h.opts.DatabaseURL(p.ID),
		"enabled":     h.svc != nil,
	}
	if h.svc == nil {
		httpx.WriteJSON(w, http.StatusOK, out)
		return
	}
	t := core.Target{Name: p.ID + core.DatabaseSuffix, ProjectID: p.ID, Project: p.Number}
	size, err := h.svc.SizeOf(r.Context(), t, "/", infoRows)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writes, err := h.svc.WriteCount(r.Context(), t)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out["devOpenRules"] = h.opts.DevOpenRules
	out["writeCount"] = writes
	out["size"] = map[string]any{"rows": size.Rows, "bytes": size.Bytes, "capped": size.Capped}
	out["limits"] = map[string]any{
		"keyBytes":    core.MaxPathBytes,
		"pathBytes":   core.MaxPathBytes,
		"depth":       core.MaxDepth,
		"stringBytes": h.svc.Limits().StringBytes,
		"writeBytes":  maxWriteBody,
		"exportBytes": maxExportBytes,
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}
