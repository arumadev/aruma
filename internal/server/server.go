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

	"github.com/osama300/aurma-project/internal/config"
	"github.com/osama300/aurma-project/internal/platform/apierr"
	"github.com/osama300/aurma-project/internal/platform/httpx"
	"github.com/osama300/aurma-project/internal/platform/metrics"
	"github.com/osama300/aurma-project/internal/store"
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
}

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
	if s.handlers.RTDB == nil {
		return main
	}
	// The Realtime Database is routed by host before path (ADR-018), and its bodies may be
	// larger than other APIs' (ADR-019).
	rtdb := httpx.CommonWithLimit(s.securityHeaders(s.metrics.Instrument("rtdb", s.handlers.RTDB)),
		s.log, s.cfg.TrustedProxyPrefixes(), s.cfg.RTDB.Limits.RESTWriteBytes)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.isRTDB(r) {
			rtdb.ServeHTTP(w, r)
			return
		}
		main.ServeHTTP(w, r)
	})
}

// isRTDB reports whether a request belongs to the Realtime Database (ADR-018).
func (s *Server) isRTDB(r *http.Request) bool {
	if d := s.cfg.RTDB.Domain; d != "" {
		host := r.Host
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
		mux := http.NewServeMux()
		mux.Handle("GET /metrics", s.metrics.Handler())
		servers = append(servers, &http.Server{
			Addr:              s.cfg.MetricsListen,
			Handler:           mux,
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
