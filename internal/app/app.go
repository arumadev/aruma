// Package app wires the server together from its configuration.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/osama300/aurma-project/internal/admin/console"
	"github.com/osama300/aurma-project/internal/admin/mgmt"
	"github.com/osama300/aurma-project/internal/admin/oauth"
	"github.com/osama300/aurma-project/internal/config"
	"github.com/osama300/aurma-project/internal/platform/authn"
	"github.com/osama300/aurma-project/internal/platform/crypto"
	"github.com/osama300/aurma-project/internal/platform/metrics"
	"github.com/osama300/aurma-project/internal/server"
	"github.com/osama300/aurma-project/internal/store"
	"github.com/osama300/aurma-project/internal/tenancy/core"
)

// App is a fully wired server.
type App struct {
	Config  *config.Config
	Service *core.Service
	Server  *server.Server
	Console *console.API
	Mgmt    *mgmt.API
	log     *slog.Logger
}

// Option adjusts wiring, mainly for tests.
type Option func(*settings)

type settings struct {
	argon2 crypto.Argon2Params
}

// WithArgon2 overrides the password hashing cost. Tests use cheap parameters.
func WithArgon2(p crypto.Argon2Params) Option { return func(s *settings) { s.argon2 = p } }

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
	svc := core.New(pool, keys, crypto.NewHasher(st.argon2, argon2Concurrency), core.Options{
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
	consoleAPI := console.New(svc, auth, logger, console.Options{Version: version, SecureCookies: cfg.SecureCookies()})
	mgmtAPI := mgmt.New(svc, auth, logger)

	srv := server.New(cfg, logger, pool, metrics.New(version), server.Handlers{
		Console: consoleAPI.Handler(),
		Mgmt:    mgmtAPI.Handler(),
		OAuth:   oauth.New(svc, logger).Handler(),
	})
	return &App{Config: cfg, Service: svc, Server: srv, Console: consoleAPI, Mgmt: mgmtAPI, log: logger}, nil
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

// Serve runs the server until ctx is cancelled. It refuses to start on an outdated schema,
// and never migrates it itself (ADR-009).
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
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
