// Package gcs serves Cloud Storage to the Admin SDKs over the GCS JSON API: /storage/v1,
// /upload/storage/v1 and /download/storage/v1 (ADR-022 §1, §14). Every request needs a service
// account's Bearer token, bound to its project: no emulator mode, no trusted network.
package gcs

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/services/storage/core"
	"github.com/arumadev/aruma/internal/services/storage/wire"
	tcore "github.com/arumadev/aruma/internal/tenancy/core"
)

// Projects lets an admin credential prove it may act on a project: the control plane's
// ownership gate.
type Projects interface {
	GetProject(ctx context.Context, p *authn.Principal, ref string) (tcore.Project, error)
}

// Options configure the API.
type Options struct {
	// PublicURL is the server's address without a trailing slash: the session Location and
	// the resources' links are absolute from it.
	PublicURL string
	// Credentials verifies the Bearer access tokens of service accounts.
	Credentials authn.Verifier
	// Projects checks that a credential may act on the bucket's project.
	Projects Projects
}

// API is the GCS JSON endpoint.
type API struct {
	svc  *core.Service
	opts Options
	log  *slog.Logger
}

// New returns the API.
func New(svc *core.Service, opts Options, log *slog.Logger) *API {
	return &API{svc: svc, opts: opts, log: log}
}

// Handler returns the handler of every request under the three GCS prefixes.
//
// Until S4-A routes it, every path answers 501.
func (a *API) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=UTF-8")
		w.WriteHeader(http.StatusNotImplemented)
		_ = json.NewEncoder(w).Encode(wire.GCSError{Error: wire.GCSErrorBody{
			Code: http.StatusNotImplemented, Message: "Not Implemented.",
			Errors: []wire.GCSErrorItem{{Domain: "global", Reason: "notImplemented", Message: "Not Implemented."}},
		}})
	})
}
