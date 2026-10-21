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

	"github.com/arumadev/aruma/internal/admin/console"
	"github.com/arumadev/aruma/internal/admin/mgmt"
	"github.com/arumadev/aruma/internal/admin/oauth"
	"github.com/arumadev/aruma/internal/admin/web"
	"github.com/arumadev/aruma/internal/config"
	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/platform/consolemount"
	"github.com/arumadev/aruma/internal/platform/crypto"
	"github.com/arumadev/aruma/internal/platform/idtoken"
	"github.com/arumadev/aruma/internal/platform/metrics"
	"github.com/arumadev/aruma/internal/server"
	authapi "github.com/arumadev/aruma/internal/services/auth/api"
	authconsole "github.com/arumadev/aruma/internal/services/auth/console"
	identity "github.com/arumadev/aruma/internal/services/auth/core"
	rtdbapi "github.com/arumadev/aruma/internal/services/rtdb/api"
	rtdbconsole "github.com/arumadev/aruma/internal/services/rtdb/console"
	rtdb "github.com/arumadev/aruma/internal/services/rtdb/core"
	"github.com/arumadev/aruma/internal/store"
	"github.com/arumadev/aruma/internal/tenancy/core"
)

// App is a fully wired server.
type App struct {
	Config  *config.Config
	Service *core.Service
	Auth    *identity.Service
	RTDB    *rtdb.Service // nil while Realtime Database is disabled

	// Auth's side of the ID token contract (platform/idtoken), for the services that accept
	// its tokens: the Realtime Database.
	IDTokens    idtoken.Verifier
	Revocations idtoken.Revocations

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
		RTDBDomain:      cfg.RTDB.Domain,
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
	idTokens, revocations := authSvc.IDTokenVerifier(), authSvc.Revocations()
	var rtdbSvc *rtdb.Service
	if cfg.RTDB.Enabled {
		l := cfg.RTDB.Limits
		rtdbSvc = rtdb.New(rtdb.NewPGStore(pool), svc, rtdb.Options{
			Limits: rtdb.Limits{
				MessageBytes: l.MessageBytes, UnauthenticatedMessageBytes: l.UnauthenticatedMessageBytes,
				InflightBytes: l.InflightBytes, RESTWriteBytes: l.RESTWriteBytes, StringBytes: l.StringBytes,
				WriteBytesPerMinute: l.WriteBytesPerMinute, ConnectionsPerProject: l.ConnectionsPerProject,
				ConnectionsPerIP: l.ConnectionsPerIP, OutboxBytes: l.OutboxBytes, ReadRows: l.ReadRows,
			},
			DevOpenRules: cfg.RTDB.DevOpenRules,
			// Users sign in with Auth's ID tokens, and lose their sessions when Auth revokes
			// them (ADR-015: Auth knows nothing of the Realtime Database).
			IDTokens:    idTokens,
			Revocations: revocations,
		}, logger)
	}
	consoleAPI := console.New(svc, auth, logger, console.Options{
		Version: version, SecureCookies: cfg.SecureCookies(),
		Mounts: []func(*consolemount.Mount){
			authconsole.Routes(authSvc),
			rtdbconsole.Routes(rtdbSvc, rtdbconsole.Options{DatabaseURL: svc.DatabaseURL, DevOpenRules: cfg.RTDB.DevOpenRules}),
		},
	})
	mgmtAPI := mgmt.New(svc, auth, logger, mgmt.Options{RTDB: rtdbSvc != nil})
	dashboard, err := web.New(web.Embedded())
	if err != nil {
		return nil, fmt.Errorf("dashboard: %w", err)
	}

	handlers := server.Handlers{
		Console: consoleAPI.Handler(),
		Mgmt:    mgmtAPI.Handler(),
		OAuth:   oauth.New(svc, logger).Handler(),
		Auth:    authapi.New(authSvc, auth, logger).Handler(),
		Static:  dashboard,
	}
	if rtdbSvc != nil {
		rtdbAPI := rtdbapi.New(rtdbSvc, rtdbapi.Options{
			Domain:         cfg.RTDB.Domain,
			TrustedProxies: cfg.TrustedProxyPrefixes(),
			Credentials:    svc,
			Projects:       svc,
			AllowedOrigins: cfg.RTDB.AllowedOrigins,
		}, logger)
		handlers.RTDB, handlers.TLSAsk = rtdbAPI, rtdbAPI.TLSAsk()
		// A purged project's databases go with it.
		svc.OnProjectPurged(rtdbSvc.PurgeProject)
	}

	srv := server.New(cfg, logger, pool, metrics.New(version), handlers)
	return &App{Config: cfg, Service: svc, Auth: authSvc, RTDB: rtdbSvc, IDTokens: idTokens, Revocations: revocations,
		Server: srv, Console: consoleAPI, Mgmt: mgmtAPI, log: logger}, nil
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
	if cfg.RTDB.Enabled && cfg.RTDB.Domain == "" && cfg.PublicBaseURL().Scheme == "https" {
		// ADR-018: ?ns= on the main host breaks the iOS, Admin Go and Admin Python SDKs.
		logger.Warn("rtdb.domain is not set: databases are reached with ?ns= on public_url, which suits local development only; set rtdb.domain")
	}
	if cfg.RTDB.DevOpenRules {
		logger.Warn("rtdb.dev_open_rules is on: every client on this machine may read and write every Realtime Database, whatever its rules")
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
	runErr := a.Server.Run(ctx)
	if a.RTDB != nil {
		// The connections are closed and their onDisconnect writes queued; let the writers
		// finish them.
		stop, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := a.RTDB.Close(stop); err != nil {
			logger.Warn("rtdb: shutdown", "error", err)
		}
	}
	if runErr != nil {
		return fmt.Errorf("serve: %w", runErr)
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
