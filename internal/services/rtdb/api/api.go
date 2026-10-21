// Package api serves Aruma's Realtime Database to the Firebase SDKs: the WebSocket protocol
// at /.ws (ws.go, conn.go, dispatch.go), long polling at /.lp (lp.go), and REST at
// /<path>.json (rest.go). A request names its database by host, https://<ns>.<rtdb.domain>, or on the
// main host by ?ns=<ns> (ADR-018).
//
// The protocol session (session.go) is separate from its transport: it decodes requests,
// orders replies and fills a byte-bounded outbox that a transport drains. A WebSocket is one
// transport; the long poll is another, able to hand its session over to a WebSocket.
package api

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"

	"github.com/osama300/aurma-project/internal/platform/authn"
	"github.com/osama300/aurma-project/internal/platform/httpx"
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
	// TrustedProxies are the proxies whose X-Forwarded-Host and X-Forwarded-For are
	// believed (trusted_proxies).
	TrustedProxies []netip.Prefix
	// Credentials verifies the admin credentials an Admin SDK or the CLI signs in with.
	Credentials authn.Verifier
	// Projects checks that a credential may act on the project.
	Projects Projects
	// AllowedOrigins, when set, lists the hosts whose web pages may connect besides a
	// database's own host (rtdb.allowed_origins). Empty allows every origin, as Firebase does.
	AllowedOrigins []string
}

// API is the Realtime Database endpoint.
type API struct {
	svc  *core.Service
	opts Options
	log  *slog.Logger

	// inflight is the bytes of partly received messages on all connections.
	inflight *budget
	// perIP counts open connections per client address.
	perIP *ipCounter

	mu           sync.Mutex
	conns        map[transport]struct{}
	shuttingDown bool
	wg           sync.WaitGroup
	// lps are the long-poll connections by id, lpSessions by session id (lp.go).
	lps        map[string]*lpConn
	lpSessions map[string]*lpConn
	lpSeq      int64

	// pushIDs makes the keys of REST POSTs.
	pushIDs core.PushIDs
}

// transport is an open connection that Shutdown closes.
type transport interface {
	shutdown()
}

// New returns the API.
func New(svc *core.Service, opts Options, logger *slog.Logger) *API {
	opts.Domain = strings.ToLower(opts.Domain)
	lim := svc.Limits()
	return &API{
		svc: svc, opts: opts, log: logger, conns: map[transport]struct{}{},
		lps: map[string]*lpConn{}, lpSessions: map[string]*lpConn{},
		inflight: &budget{left: lim.InflightBytes},
		perIP:    &ipCounter{limit: lim.ConnectionsPerIP, n: map[netip.Addr]int{}},
	}
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
		e := core.AsError(err)
		writeError(w, e.HTTP, e.Message)
		return
	}
	switch {
	case r.URL.Path == "/.ws":
		a.serveWS(w, r, target)
	case r.URL.Path == "/.lp":
		a.serveLP(w, r, target)
	case r.URL.Path == "/.settings/rules.json":
		a.serveRules(w, r, target)
	case strings.HasPrefix(r.URL.Path, "/.") && !strings.HasSuffix(r.URL.Path, ".json"):
		// The emulator's answer to the dot paths it does not serve (/.info, /.inspect, ...).
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("not found"))
	case strings.HasSuffix(r.URL.Path, ".json"):
		a.serveREST(w, r, target)
	default:
		// The emulator's answer to a path without .json.
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("Append .json to your URL to use the REST API."))
	}
}

// host is the host the client connected to, through a trusted proxy's X-Forwarded-Host.
func (a *API) host(r *http.Request) string { return httpx.Host(r, a.opts.TrustedProxies) }

// namespace returns the database a request names (ADR-018): the first label of a host under
// rtdb.domain, else ?ns= on the main host. Both present and different is an error, so a
// client never writes to another database than its URL says.
func (a *API) namespace(r *http.Request) (string, error) {
	fromHost := a.hostNamespace(a.host(r))
	fromQuery := r.URL.Query().Get("ns")
	switch {
	case fromHost != "" && fromQuery != "" && fromHost != fromQuery:
		return "", errors.New("The namespace in ?ns= (" + fromQuery + ") does not match the host (" + fromHost + ").")
	case fromHost != "":
		return fromHost, nil
	case fromQuery != "":
		return fromQuery, nil
	}
	return "", errors.New("Specify the database: use https://<project>-default-rtdb." + a.domainOrExample() + " or ?ns=<project>-default-rtdb.")
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
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	label, ok := strings.CutSuffix(host, "."+a.opts.Domain)
	if !ok || label == "" || strings.Contains(label, ".") {
		return ""
	}
	return label
}

// budget is the shared byte budget of partly received messages (wire.Budget).
type budget struct {
	mu   sync.Mutex
	left int64
}

func (b *budget) Take(n int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if int64(n) > b.left {
		return false
	}
	b.left -= int64(n)
	return true
}

func (b *budget) Give(n int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.left += int64(n)
}

// ipCounter counts open connections per client address.
type ipCounter struct {
	mu    sync.Mutex
	limit int
	n     map[netip.Addr]int
}

func (c *ipCounter) acquire(ip netip.Addr) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.n[ip] >= c.limit {
		return false
	}
	c.n[ip]++
	return true
}

func (c *ipCounter) release(ip netip.Addr) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.n[ip]--; c.n[ip] <= 0 {
		delete(c.n, ip)
	}
}
