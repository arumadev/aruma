// Package api serves Cloud Storage to the Firebase client SDKs over Firebase REST: /v0/b/... and
// the prefixed form /firebasestorage.googleapis.com/v0/b/... that a runtime redirect produces
// (ADR-022 §1). It routes on URL.EscapedPath, so an encoded "/" stays inside an object's name,
// and every reply is free of JSON null (ADR-022 §12).
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/netip"

	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/platform/idtoken"
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
	// PublicURL is the server's address without a trailing slash: the upload session URL is
	// absolute from it, never from the Host header (ADR-022 §6.1).
	PublicURL string
	// TrustedProxies are the proxies whose X-Forwarded-For is believed, for the limits per IP.
	TrustedProxies []netip.Prefix
	// Credentials verifies the Bearer access tokens of Admin SDKs and the CLI.
	Credentials authn.Verifier
	// Projects checks that a credential may act on the bucket's project.
	Projects Projects
	// IDTokens verifies the "Authorization: Firebase <idToken>" of signed-in users.
	IDTokens idtoken.Verifier
}

// API is the Firebase REST endpoint.
type API struct {
	svc  *core.Service
	opts Options
	log  *slog.Logger
}

// New returns the API.
func New(svc *core.Service, opts Options, log *slog.Logger) *API {
	return &API{svc: svc, opts: opts, log: log}
}

// Handler returns the handler of every request under the two /v0 prefixes.
//
// Until S3-A routes it, every path answers 501.
func (a *API) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=UTF-8")
		w.WriteHeader(http.StatusNotImplemented)
		_ = json.NewEncoder(w).Encode(wire.FirebaseError{Error: wire.FirebaseErrorBody{
			Code: http.StatusNotImplemented, Message: "Not Implemented."}})
	})
}
