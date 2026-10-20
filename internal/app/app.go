// Package app wires the server together from its configuration.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/osama300/aurma-project/internal/admin/console"
	"github.com/osama300/aurma-project/internal/admin/mgmt"
	"github.com/osama300/aurma-project/internal/admin/oauth"
	"github.com/osama300/aurma-project/internal/admin/web"
	"github.com/osama300/aurma-project/internal/config"
	"github.com/osama300/aurma-project/internal/platform/authn"
	"github.com/osama300/aurma-project/internal/platform/consolemount"
	"github.com/osama300/aurma-project/internal/platform/crypto"
	"github.com/osama300/aurma-project/internal/platform/metrics"
	"github.com/osama300/aurma-project/internal/server"
	authapi "github.com/osama300/aurma-project/internal/services/auth/api"
	authconsole "github.com/osama300/aurma-project/internal/services/auth/console"
	identity "github.com/osama300/aurma-project/internal/services/auth/core"
	"github.com/osama300/aurma-project/internal/store"
	"github.com/osama300/aurma-project/internal/tenancy/core"
)

// App is a fully wired server.
type App struct {
	Config  *config.Config
	Service *core.Service
	Auth    *identity.Service
	Server  *server.Server
	Console *console.API
	Mgmt    *mgmt.API
	log     *slog.Logger
}

// Option adjusts wiring, mainly for tests.
type Option func(*settings)

type settings struct {
	argon2 crypto.Argon2Params
	idp    func(*identity.Service)
}

// WithArgon2 overrides the password hashing cost. Tests use cheap parameters.
func WithArgon2(p crypto.Argon2Params) Option { return func(s *settings) { s.argon2 = p } }

// WithIdPEndpoints points Google and Apple sign-in at stand-in providers. Tests only: no
// configuration setting reaches the provider endpoints.
func WithIdPEndpoints(google, apple identity.IdPEndpoints, client *http.Client) Option {
	return func(s *settings) {
		s.idp = func(a *identity.Service) { a.SetIdPEndpoints(google, apple, client) }
	}
}

// argon2Concurrency caps simultaneous password hashes (≈19 MiB each).
const argon2Concurrency = 4

// New wires the application on an open pool.
func New(cfg *config.Config, logger *slog.Logger, version string, pool *pgxpool.Pool, opts ...Option) (*App, error) {
	st := settings{argon2: crypto.DefaultArgon2}
	for _, o := range opts {
		o(&st)
	}
	keys, err := crypto.NewKeys(cfg.MasterKeyBytes())
	if err != nil {
		return nil, err
	}
	hasher := crypto.NewHasher(st.argon2, argon2Concurrency)
	svc := core.New(pool, keys, hasher, core.Options{
		SignupOpen:      cfg.SignupOpen,
		IdleTimeout:     cfg.Session.IdleTimeout,
		AbsoluteTimeout: cfg.Session.AbsoluteTimeout,
		PublicURL:       strings.TrimSuffix(cfg.PublicBaseURL().String(), "/"),
		ProjectDomain:   cfg.ProjectDomain,
	}, logger)

	auth := &authn.Authenticator{
		Verifier:   svc,
		Keys:       keys,
		CookieName: console.CookieName(cfg.SecureCookies()),
		Logger:     logger,
	}
	authSvc := identity.New(pool, svc, keys, hasher, strings.TrimSuffix(cfg.PublicBaseURL().String(), "/"), logger)
	authSvc.ConfigureMail(identity.MailOptions{DevMailbox: cfg.DevMailbox(), SMTPAllowPrivate: cfg.SMTPAllowPrivate()})
	if st.idp != nil {
		st.idp(authSvc)
	}
	consoleAPI := console.New(svc, auth, logger, console.Options{
		Version: version, SecureCookies: cfg.SecureCookies(),
		Mounts: []func(*consolemount.Mount){authconsole.Routes(authSvc)},
	})
	mgmtAPI := mgmt.New(svc, auth, logger)
	dashboard, err := web.New(web.Embedded())
	if err != nil {
		return nil, fmt.Errorf("dashboard: %w", err)
	}

	srv := server.New(cfg, logger, pool, metrics.New(version), server.Handlers{
		Console: consoleAPI.Handler(),
		Mgmt:    mgmtAPI.Handler(),
		OAuth:   oauth.New(svc, logger).Handler(),
		Auth:    authapi.New(authSvc, auth, logger).Handler(),
		Static:  dashboard,
	})
	return &App{Config: cfg, Service: svc, Auth: authSvc, Server: srv, Console: consoleAPI, Mgmt: mgmtAPI, log: logger}, nil
}

// AnnounceSetup prints the one-time setup code when the instance has no account yet.
func (a *App) AnnounceSetup(ctx context.Context) error {
	code, required, err := a.Service.PrepareSetup(ctx)
	if err != nil || !required {
		return err
	}
	a.log.Warn("no account exists yet: open the setup page and enter the one-time code",
		"setup_url", strings.TrimSuffix(a.Config.PublicBaseURL().String(), "/")+"/setup/",
		"code", code)
	return nil
}

// Serve runs the server until ctx is cancelled. It refuses to start on an outdated schema or
// one newer than the binary, and never migrates it itself (ADR-009).
func Serve(ctx context.Context, cfg *config.Config, logger *slog.Logger, version string) error {
	pool, err := store.Open(ctx, cfg.DatabaseURL.Reveal())
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := store.CheckCurrent(ctx, pool); err != nil {
		return err
	}
	if !cfg.SecureCookies() {
		logger.Warn("public_url is plain http on a non-loopback host: console cookies will not be Secure; put the server behind https")
	}
	a, err := New(cfg, logger, version, pool)
	if err != nil {
		return err
	}
	if err := a.AnnounceSetup(ctx); err != nil {
		return err
	}
	go a.housekeeping(ctx)
	go a.Auth.RunMailer(ctx)
	logger.Info("starting aruma", "version", version, "public_url", cfg.PublicBaseURL().String())
	if err := a.Server.Run(ctx); err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

// housekeepingEvery is how often expired deletions and tokens are purged.
const housekeepingEvery = time.Hour

// housekeeping purges what has outlived its retention, at start and then hourly.
func (a *App) housekeeping(ctx context.Context) {
	t := time.NewTicker(housekeepingEvery)
	defer t.Stop()
	for {
		if _, err := a.Service.PurgeExpired(ctx); err != nil && ctx.Err() == nil {
			a.log.Error("housekeeping", "error", err)
		}
		if err := a.Auth.Housekeep(ctx); err != nil && ctx.Err() == nil {
			a.log.Error("auth housekeeping", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
