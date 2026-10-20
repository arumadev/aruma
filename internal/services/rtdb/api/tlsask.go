package api

import (
	"errors"
	"net/http"

	"github.com/osama300/aurma-project/internal/services/rtdb/core"
)

// TLSAsk answers a TLS proxy that issues certificates on demand, such as Caddy's on_demand_tls
// ask (ADR-018 §5): GET ?domain=<name> is 200 when name is <ns>.<rtdb.domain> and ns the
// database of an existing, active project, and 404 for any other name, so a client cannot make
// the proxy request certificates for names that serve nothing. The server mounts it at
// /rtdb/tls-ask on the private metrics_listen address only: the answer tells which projects
// exist, which the public entry point never does for a stranger.
func (a *API) TLSAsk() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		ns := a.hostNamespace(r.URL.Query().Get("domain"))
		if ns == "" {
			http.Error(w, "not a database host", http.StatusNotFound)
			return
		}
		switch _, err := a.svc.Resolve(r.Context(), ns); {
		case errors.Is(err, core.ErrNoDatabase):
			http.Error(w, "no such database", http.StatusNotFound)
		case err != nil:
			// The control plane could not answer; the proxy refuses the handshake and asks
			// again on the next one.
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		default:
			_, _ = w.Write([]byte("ok\n"))
		}
	})
}
