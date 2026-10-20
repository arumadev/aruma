// Package api serves Aruma's Realtime Database to the Firebase SDKs: the WebSocket protocol
// at /.ws (ws.go, conn.go, dispatch.go), and later long polling at /.lp and REST at
// /<path>.json. A request names its database by host, https://<ns>.<rtdb.domain>, or on the
// main host by ?ns=<ns> (ADR-018).
//
// The protocol session (session.go) is separate from its transport: it decodes requests,
// orders replies and fills a byte-bounded outbox that a transport drains. A WebSocket is one
// transport; the long poll of R3 will be another, able to hand its session over to a
// WebSocket.
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/osama300/aurma-project/internal/platform/authn"
	"github.com/osama300/aurma-project/internal/services/rtdb/core"
	tcore "github.com/osama300/aurma-project/internal/tenancy/core"
)

// Projects lets an admin credential prove it may act on a project: the control plane's
// ownership gate.
type Projects interface {
	GetProject(ctx context.Context, p *authn.Principal, ref string) (tcore.Project, error)
}

// Options configure the API.
type Options struct {
	// Domain is rtdb.domain: requests to <ns>.<Domain> belong to database ns. Empty, only
	// ?ns= on the main host names a database.
	Domain string
	// Credentials verifies the admin credentials an Admin SDK or the CLI signs in with.
	Credentials authn.Verifier
	// Projects checks that a credential may act on the project.
	Projects Projects
	// AllowedOrigins lists the hosts whose web pages may open a connection to a project's
	// database, besides the database's own host. nil allows only the own host.
	AllowedOrigins func(ctx context.Context, project int64) ([]string, error)
}

// API is the Realtime Database endpoint.
type API struct {
	svc  *core.Service
	opts Options
	log  *slog.Logger

	mu           sync.Mutex
	conns        map[*wsConn]struct{}
	shuttingDown bool
	wg           sync.WaitGroup
}

// New returns the API.
func New(svc *core.Service, opts Options, logger *slog.Logger) *API {
	opts.Domain = strings.ToLower(opts.Domain)
	return &API{svc: svc, opts: opts, log: logger, conns: map[*wsConn]struct{}{}}
}

// ServeHTTP serves every request routed to the Realtime Database. The entry point also calls
// Shutdown (ws.go) when it stops.
func (a *API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ns, err := a.namespace(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	target, err := a.svc.Resolve(r.Context(), ns)
	if err != nil {
		writeError(w, http.StatusNotFound, core.AsError(err).Message)
		return
	}
	switch {
	case r.URL.Path == "/.ws":
		a.serveWS(w, r, target)
	case r.URL.Path == "/.lp":
		// Long polling comes with the listeners in R3 (ADR-020).
		writeError(w, http.StatusNotFound, "Long polling is not supported by this server yet.")
	case strings.HasSuffix(r.URL.Path, ".json"):
		// REST comes in R5.
		writeError(w, http.StatusNotImplemented, "The REST API is not supported by this server yet.")
	default:
		// The emulator's answer to a path without .json.
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("Append .json to your URL to use the REST API."))
	}
}

// errNamespace explains a request whose database cannot be told.
type errNamespace string

func (e errNamespace) Error() string { return string(e) }

// namespace returns the database a request names (ADR-018): the first label of a host under
// rtdb.domain, else ?ns= on the main host. Both present and different is an error, so a
// client never writes to another database than its URL says.
func (a *API) namespace(r *http.Request) (string, error) {
	fromHost := a.hostNamespace(r.Host)
	fromQuery := r.URL.Query().Get("ns")
	switch {
	case fromHost != "" && fromQuery != "" && fromHost != fromQuery:
		return "", errNamespace("The namespace in ?ns= (" + fromQuery + ") does not match the host (" + fromHost + ").")
	case fromHost != "":
		return fromHost, nil
	case fromQuery != "":
		return fromQuery, nil
	}
	return "", errNamespace("Specify the database: use https://<project>-default-rtdb." + a.domainOrExample() + " or ?ns=<project>-default-rtdb.")
}

func (a *API) domainOrExample() string {
	if a.opts.Domain != "" {
		return a.opts.Domain
	}
	return "<rtdb.domain>"
}

// hostNamespace returns ns for a host <ns>.<domain>, else "".
func (a *API) hostNamespace(host string) string {
	if a.opts.Domain == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	label, ok := strings.CutSuffix(strings.ToLower(host), "."+a.opts.Domain)
	if !ok || label == "" || strings.Contains(label, ".") {
		return ""
	}
	return label
}

// writeError answers in Firebase's REST error shape.
func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(status)
	b, _ := json.Marshal(map[string]string{"error": msg})
	_, _ = w.Write(b)
}
