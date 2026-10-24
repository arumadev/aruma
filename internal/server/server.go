// Package server is the single public entry point (ADR-008): one listener, routing by path,
// shared middleware and security headers, plus the private metrics listener.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arumadev/aruma/internal/config"
	"github.com/arumadev/aruma/internal/platform/apierr"
	"github.com/arumadev/aruma/internal/platform/httpx"
	"github.com/arumadev/aruma/internal/platform/metrics"
	"github.com/arumadev/aruma/internal/store"
)

// Handlers are the APIs mounted on the entry point. A nil handler answers NOT_FOUND.
type Handlers struct {
	Console http.Handler // /console/v1/
	Mgmt    http.Handler // /v1beta1/
	OAuth   http.Handler // /oauth2/
	Auth    http.Handler // AuthPrefixes: the Identity Toolkit, Secure Token and discovery paths (ADR-014)
	Static  http.Handler // everything else: the dashboard
	// RTDB is the Realtime Database (ADR-018): every request to a host under rtdb.domain, and
	// on the main host /.ws, /.lp and the .json paths that carry ?ns=. Nil while it is
	// disabled. If it has a Shutdown(context.Context) error method, Run calls it on shutdown
	// to close its hijacked connections, which http.Server.Shutdown does not see.
	RTDB http.Handler
	// TLSAsk answers GET /rtdb/tls-ask on the private metrics_listen address only, for a TLS
	// proxy that issues the database hosts' certificates on demand (ADR-018 §5). Nil while the
	// Realtime Database is disabled.
	TLSAsk http.Handler
	// Storage is Cloud Storage's Firebase REST API, under StoragePrefixes, and GCS its GCS JSON
	// API, under GCSPrefixes (ADR-022 §1). Their bodies may be as large as an object. Nil while
	// Storage is disabled: the paths then fall through to the rest of the entry point.
	Storage http.Handler
	GCS     http.Handler
}

// StoragePrefixes are the paths routed to Handlers.Storage: the unified download URL's /v0, and
// the prefixed form a runtime redirect of firebasestorage.googleapis.com sends (ADR-022 §1).
var StoragePrefixes = []string{"/v0/", "/firebasestorage.googleapis.com/v0/"}

// GCSPrefixes are the paths routed to Handlers.GCS: the Admin SDKs' apiEndpoint (ADR-022 §1).
var GCSPrefixes = []string{"/storage/v1/", "/upload/storage/v1/", "/download/storage/v1/"}

// shutdowner is a handler that holds connections of its own.
type shutdowner interface {
	Shutdown(ctx context.Context) error
}

// AuthPrefixes are the paths routed to Handlers.Auth.
var AuthPrefixes = []string{
	"/identitytoolkit.googleapis.com/",
	"/securetoken.googleapis.com/",
	"/www.googleapis.com/identitytoolkit/",
	"/issuers/",
	"/session-issuers/",
	"/__/",       // authDomain pages: /__/auth/*, /__/firebase/init.json
	"/emulator/", // only /emulator/auth/{handler,iframe} exist; every other path is 404
}

// Server owns the public and metrics listeners.
type Server struct {
	cfg      *config.Config
	log      *slog.Logger
	pool     *pgxpool.Pool
	metrics  *metrics.Metrics
	handlers Handlers
}

// New assembles the server.
func New(cfg *config.Config, logger *slog.Logger, pool *pgxpool.Pool, m *metrics.Metrics, h Handlers) *Server {
	return &Server{cfg: cfg, log: logger, pool: pool, metrics: m, handlers: h}
}

// Handler returns the public entry point.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/console/v1/", s.metrics.Instrument("console", apiOnly(s.handlers.Console)))
	mux.Handle("/v1beta1/", s.metrics.Instrument("mgmt", apiOnly(s.handlers.Mgmt)))
	// The Realtime Database's management API (internal/admin/mgmt/rtdb.go).
	mux.Handle("/v1beta/", s.metrics.Instrument("mgmt", apiOnly(s.handlers.Mgmt)))
	if s.handlers.Storage != nil {
		// firebaserules, Service Usage and defaultBucket for the firebase CLI (ADR-022 §1, §15):
		// mounted with Storage, until Firestore needs them too, so a server without Storage
		// routes exactly as before.
		mux.Handle("/v1/projects/", s.metrics.Instrument("mgmt", apiOnly(s.handlers.Mgmt)))
		mux.Handle("/v1alpha/projects/", s.metrics.Instrument("mgmt", apiOnly(s.handlers.Mgmt)))
	}
	mux.Handle("/oauth2/", s.metrics.Instrument("oauth", apiOnly(s.handlers.OAuth)))
	for _, prefix := range AuthPrefixes {
		mux.Handle(prefix, s.metrics.Instrument("auth", apiOnly(s.handlers.Auth)))
	}
	mux.Handle("GET /healthz", s.metrics.Instrument("health", http.HandlerFunc(s.healthz)))
	mux.Handle("GET /readyz", s.metrics.Instrument("health", http.HandlerFunc(s.readyz)))
	static := s.handlers.Static
	if static == nil {
		static = http.NotFoundHandler()
	}
	mux.Handle("/", s.metrics.Instrument("static", static))
	main := httpx.Common(s.securityHeaders(mux), s.log, s.cfg.TrustedProxyPrefixes())
	storage := s.storageMux()
	if s.handlers.RTDB == nil && storage == nil {
		return main
	}
	// The Realtime Database is routed by host before path (ADR-018), and its bodies may be
	// larger than other APIs' (ADR-019).
	var rtdb http.Handler
	if s.handlers.RTDB != nil {
		rtdb = httpx.CommonWithLimit(s.securityHeaders(s.metrics.Instrument("rtdb", s.handlers.RTDB)),
			s.log, s.cfg.TrustedProxyPrefixes(), s.cfg.RTDB.Limits.RESTWriteBytes)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case rtdb != nil && s.isRTDB(r):
			rtdb.ServeHTTP(w, r)
		case storage != nil && storage.owns(r.URL.Path):
			storage.ServeHTTP(w, r)
		default:
			main.ServeHTTP(w, r)
		}
	})
}

// storageRoutes are Storage's paths, which skip the other APIs' 1 MiB body cap: their cap is
// object_bytes and the slack of a multipart upload (ADR-022 §1). They dispatch on the prefix
// alone, without a ServeMux, which would clean or redirect a path carrying an object's name.
type storageRoutes struct {
	http.Handler
	apis []storageAPI
}

type storageAPI struct {
	h        http.Handler
	prefixes []string
}

// route returns the API a path belongs to, or nil.
func (sr *storageRoutes) route(path string) http.Handler {
	for _, api := range sr.apis {
		for _, p := range api.prefixes {
			if strings.HasPrefix(path, p) {
				return api.h
			}
		}
	}
	return nil
}

// owns reports whether a path belongs to a mounted Storage API.
func (sr *storageRoutes) owns(path string) bool { return sr.route(path) != nil }

// storageMux returns Storage's routes, or nil while Storage is disabled.
func (s *Server) storageMux() *storageRoutes {
	sr := &storageRoutes{}
	if h := s.handlers.Storage; h != nil {
		sr.apis = append(sr.apis, storageAPI{s.metrics.Instrument("storage", h), StoragePrefixes})
	}
	if h := s.handlers.GCS; h != nil {
		sr.apis = append(sr.apis, storageAPI{s.metrics.Instrument("gcs", h), GCSPrefixes})
	}
	if sr.apis == nil {
		return nil
	}
	dispatch := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { sr.route(r.URL.Path).ServeHTTP(w, r) })
	// An object, and a multipart upload's JSON part and boundaries; each handler enforces its
	// exact limit (ADR-022 §6.4).
	limit := s.cfg.Storage.Limits.BodyCap()
	sr.Handler = httpx.CommonWithLimit(s.securityHeaders(dispatch), s.log, s.cfg.TrustedProxyPrefixes(), limit)
	return sr
}

// isRTDB reports whether a request belongs to the Realtime Database (ADR-018).
func (s *Server) isRTDB(r *http.Request) bool {
	if d := s.cfg.RTDB.Domain; d != "" {
		// The host the client asked for, through a trusted proxy's X-Forwarded-Host.
		host := httpx.Host(r, s.cfg.TrustedProxyPrefixes())
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if strings.HasSuffix(strings.ToLower(host), "."+d) {
			return true
		}
	}
	switch p := r.URL.Path; {
	case p == "/.ws" || p == "/.lp":
		return true
	case strings.HasSuffix(p, ".json"):
		return r.URL.Query().Has("ns")
	}
	return false
}

// securityHeaders applies the headers every response carries (ADR-010 S-8, S-9). Only the
// Auth client and discovery routes add CORS headers (ADR-014). API responses get a CSP that
// forbids everything; the dashboard handler replaces it with its own per-page policy.
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	hsts := s.cfg.PublicBaseURL().Scheme == "https"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		if hsts {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

func apiOnly(h http.Handler) http.Handler {
	if h != nil {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apierr.Write(w, apierr.New(apierr.NotFound, "Requested entity was not found."))
	})
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.pool.Ping(ctx); err != nil {
		s.log.Warn("readiness: database unreachable", "error", err)
		httpx.WriteJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable", "reason": "database"})
		return
	}
	// A schema newer than the binary keeps a running server ready: `aruma migrate up` runs
	// before the new version starts (ADR-009), and the old instances serve until replaced.
	// Startup refuses such a schema instead (ADR-015).
	if err := store.CheckCurrent(ctx, s.pool); err != nil && !errors.Is(err, store.ErrSchemaNewer) {
		httpx.WriteJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable", "reason": "schema"})
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// PrivateHandler serves the private metrics_listen address: /metrics, and the TLS proxy's
// ask when the Realtime Database is on. Nothing here is reachable from the public entry point.
func (s *Server) PrivateHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", s.metrics.Handler())
	if s.handlers.TLSAsk != nil {
		mux.Handle("GET /rtdb/tls-ask", s.handlers.TLSAsk)
	}
	return mux
}

// Run serves until ctx is cancelled, then shuts down gracefully.
func (s *Server) Run(ctx context.Context) error {
	public := &http.Server{
		Addr:              s.cfg.Listen,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		MaxHeaderBytes:    64 << 10,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelWarn),
	}
	servers := []*http.Server{public}
	if s.cfg.MetricsListen != "" {
		servers = append(servers, &http.Server{
			Addr:              s.cfg.MetricsListen,
			Handler:           s.PrivateHandler(),
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       30 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       120 * time.Second,
			MaxHeaderBytes:    16 << 10,
		})
	}

	errc := make(chan error, len(servers))
	for _, srv := range servers {
		ln, err := net.Listen("tcp", srv.Addr)
		if err != nil {
			for _, other := range servers {
				_ = other.Close()
			}
			return fmt.Errorf("listen %s: %w", srv.Addr, err)
		}
		s.log.Info("listening", "addr", ln.Addr().String())
		go func(srv *http.Server, ln net.Listener) {
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errc <- err
			}
		}(srv, ln)
	}

	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-errc:
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	if sh, ok := s.handlers.RTDB.(shutdowner); ok {
		wg.Go(func() {
			if err := sh.Shutdown(shutdown); err != nil {
				s.log.Warn("shutdown", "handler", "rtdb", "error", err)
			}
		})
	}
	for _, srv := range servers {
		if err := srv.Shutdown(shutdown); err != nil {
			s.log.Warn("shutdown", "addr", srv.Addr, "error", err)
		}
	}
	wg.Wait()
	return runErr
}
