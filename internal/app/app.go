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
	"github.com/arumadev/aruma/internal/platform/ratelimit"
	"github.com/arumadev/aruma/internal/platform/rules"
	"github.com/arumadev/aruma/internal/server"
	authapi "github.com/arumadev/aruma/internal/services/auth/api"
	authconsole "github.com/arumadev/aruma/internal/services/auth/console"
	identity "github.com/arumadev/aruma/internal/services/auth/core"
	documents "github.com/arumadev/aruma/internal/services/documents/core"
	rtdbapi "github.com/arumadev/aruma/internal/services/rtdb/api"
	rtdbconsole "github.com/arumadev/aruma/internal/services/rtdb/console"
	rtdb "github.com/arumadev/aruma/internal/services/rtdb/core"
	storageapi "github.com/arumadev/aruma/internal/services/storage/api"
	"github.com/arumadev/aruma/internal/services/storage/blob"
	storageconsole "github.com/arumadev/aruma/internal/services/storage/console"
	storage "github.com/arumadev/aruma/internal/services/storage/core"
	"github.com/arumadev/aruma/internal/services/storage/gcs"
	"github.com/arumadev/aruma/internal/store"
	"github.com/arumadev/aruma/internal/tenancy/core"
)

// App is a fully wired server.
type App struct {
	Config  *config.Config
	Service *core.Service
	Auth    *identity.Service
	RTDB    *rtdb.Service    // nil while Realtime Database is disabled
	Storage *storage.Service // nil while Cloud Storage is disabled
	// Documents is the Document Database; nil while it is disabled.
	Documents *Documents

	// Auth's side of the ID token contract (platform/idtoken), for the services that accept
	// its tokens: the Realtime Database, and Storage, which also asks for a user's live state
	// when an upload finalizes (Users).
	IDTokens    idtoken.Verifier
	Revocations idtoken.Revocations
	Users       idtoken.Users

	// The Firestore and Storage rules engine's limits, and the one cap on the memory of every
	// evaluation in the process (ADR-021 §4). Storage takes both with rules.NoDocuments{}; the
	// rules API takes the limits alone, since its :test evaluates nothing.
	RulesLimits   rules.Limits
	RulesInflight *rules.Inflight

	Server  *server.Server
	Console *console.API
	Mgmt    *mgmt.API
	log     *slog.Logger
}

// Option adjusts wiring, mainly for tests.
type Option func(*settings)

type settings struct {
	argon2           crypto.Argon2Params
	idp              func(*identity.Service)
	rtdbWriteTimeout time.Duration
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

// WithRTDBWriteTimeout bounds the write of one Realtime Database frame. Tests only: the
// server's own is 30 seconds.
func WithRTDBWriteTimeout(d time.Duration) Option {
	return func(s *settings) { s.rtdbWriteTimeout = d }
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
	rulesLimits := cfg.RulesEngineLimits()
	rulesInflight := rules.NewInflight(rulesLimits.InflightBytes)
	svc := core.New(pool, keys, hasher, core.Options{
		SignupOpen:      cfg.SignupOpen,
		IdleTimeout:     cfg.Session.IdleTimeout,
		AbsoluteTimeout: cfg.Session.AbsoluteTimeout,
		PublicURL:       strings.TrimSuffix(cfg.PublicBaseURL().String(), "/"),
		ProjectDomain:   cfg.ProjectDomain,
		RTDBDomain:      cfg.RTDB.Domain,
		RulesLimits:     rulesLimits,
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
	idTokens, revocations, users := authSvc.IDTokenVerifier(), authSvc.Revocations(), authSvc.Users()
	m := metrics.New(version)
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
	var storageSvc *storage.Service
	if cfg.Storage.Enabled {
		disk, err := blob.NewDisk(cfg.Storage.DataDir)
		if err != nil {
			return nil, fmt.Errorf("storage: %w", err)
		}
		// Storage resolves buckets, rulesets and ownership through the control plane, reads no
		// Firestore document until v0.4, shares the process's one cap on rules memory, and asks
		// Auth for a user's live state when an upload finalizes (ADR-022 §7, §8). It opens
		// nothing here: Boot takes the data directory before the listener opens (Serve).
		storageMetrics := storage.NewMetrics()
		storageSvc, err = storage.New(context.Background(), pool, disk, storage.Options{
			Limits:    storage.Limits(cfg.Storage.Limits),
			Keys:      keys,
			Projects:  svc,
			Documents: rules.NoDocuments{},
			Inflight:  rulesInflight,
			Users:     users,
			DataDir:   cfg.Storage.DataDir,
			GCGrace:   cfg.Storage.GCGrace,
			Metrics:   storageMetrics,
		}, logger)
		if err != nil {
			return nil, fmt.Errorf("storage: %w", err)
		}
		m.Registry.MustRegister(storageMetrics.Collectors()...)
	}
	publicURL := strings.TrimSuffix(cfg.PublicBaseURL().String(), "/")
	consoleAPI := console.New(svc, auth, logger, console.Options{
		Version: version, SecureCookies: cfg.SecureCookies(),
		Mounts: []func(*consolemount.Mount){
			authconsole.Routes(authSvc),
			rtdbconsole.Routes(rtdbSvc, rtdbconsole.Options{DatabaseURL: svc.DatabaseURL, DevOpenRules: cfg.RTDB.DevOpenRules}),
			storageconsole.Routes(storageSvc, storageconsole.Options{
				PublicURL: publicURL, ProjectDomain: cfg.ProjectDomain, Rules: svc, Audit: svc.Audit,
			}),
		},
	})
	mgmtAPI := mgmt.New(svc, auth, logger, mgmt.Options{
		RTDB: rtdbSvc != nil, Storage: storageSvc != nil, RulesLimits: rulesLimits,
	})
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
			WriteTimeout:   st.rtdbWriteTimeout,
		}, logger)
		handlers.RTDB, handlers.TLSAsk = rtdbAPI, rtdbAPI.TLSAsk()
		// A purged project's databases go with it.
		svc.OnProjectPurged(rtdbSvc.PurgeProject)
	}
	if storageSvc != nil {
		// upload_requests_per_ip is one budget per address across both surfaces (ADR-022 §17):
		// a client cannot double it by sending writes to /v0 and GCS at once.
		writes := new(ratelimit.InFlight)
		handlers.Storage = storageapi.New(storageSvc, storageapi.Options{
			PublicURL:      publicURL,
			TrustedProxies: cfg.TrustedProxyPrefixes(),
			Credentials:    svc,
			Projects:       svc,
			IDTokens:       idTokens,
			Writes:         writes,
		}, logger).Handler()
		handlers.GCS = gcs.New(storageSvc, gcs.Options{
			PublicURL:      publicURL,
			TrustedProxies: cfg.TrustedProxyPrefixes(),
			Credentials:    svc,
			Writes:         writes,
		}, logger).Handler()
		// A purged project's objects, sessions and files go with it.
		svc.OnProjectPurged(storageSvc.PurgeProject)
	}

	var docs *Documents
	if cfg.Documents.Enabled {
		docs = newDocuments(cfg, svc, pool, m, logger)
		docs.mount(&handlers)
	}

	srv := server.New(cfg, logger, pool, m, handlers)
	return &App{Config: cfg, Service: svc, Auth: authSvc, RTDB: rtdbSvc, Storage: storageSvc, Documents: docs,
		IDTokens: idTokens, Revocations: revocations, Users: users,
		RulesLimits: rulesLimits, RulesInflight: rulesInflight,
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
	// The pool closes last, after every service has drained its writes on it (below).
	defer pool.Close()
	if err := store.CheckCurrent(ctx, pool); err != nil {
		return err
	}
	if !cfg.SecureCookies() {
		logger.Warn("public_url is plain http on a non-loopback host: console cookies will not be Secure; put the server behind https")
		if cfg.Storage.Enabled {
			// Browsers send no Fetch Metadata over plain http, so media's refusal of an
			// uploaded script loaded from the dashboard's origin never fires (ADR-022 §10.1).
			logger.Warn("storage is enabled on plain http: browsers send no Sec-Fetch-* headers, so an uploaded file loaded as a script or style from the dashboard's origin is not refused; put the server behind https")
		}
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
	// Everything started below ends with the server: on a cancelled ctx, and also when the
	// server stops by itself (a listen that fails, a listener that breaks), which ctx alone would
	// not say, so Storage's Run would hold its gate and Close would wait for it in vain.
	ctx, endWork := context.WithCancel(ctx)
	defer endWork()
	if a.Storage != nil {
		// Before the listener opens (ADR-022 §5.1): the data directory's lock, which a new
		// instance waits for while the old one runs, its binding to this database, the list of
		// files that may be orphans, and the quota reconciled. A load balancer sees the instance
		// only once it holds the directory.
		if err := a.Storage.Boot(ctx); err != nil {
			return fmt.Errorf("storage: %w", err)
		}
		// After: the orphans deleted, the GC, the expiry of sessions and the periodic sweep.
		go func() {
			if err := a.Storage.Run(ctx); err != nil && ctx.Err() == nil {
				logger.Error("storage", "error", err)
			}
		}()
	}
	if a.Documents != nil {
		// Before the listener opens (ADR-024 §7): the single-instance lock, for up to 30
		// seconds, which a new instance waits for while the old one drains.
		if err := a.Documents.Boot(ctx, documents.InstanceOptions{}); err != nil {
			return fmt.Errorf("documents: %w", err)
		}
		defer a.Documents.Close()
	}
	if cfg.Documents.DevOpenRules {
		logger.Warn("documents.dev_open_rules is on: every client on this machine may read and write every Document Database")
	}
	go a.housekeeping(ctx)
	go a.Auth.RunMailer(ctx)
	logger.Info("starting aruma", "version", version, "public_url", cfg.PublicBaseURL().String())
	runErr := a.Server.Run(ctx)
	endWork()
	// The shutdown, once the server has drained its requests and the background work is told to
	// end: each data service finishes its writes while the pool is still open, and the pool
	// closes only after both (its defer).
	if a.RTDB != nil {
		// The connections are closed and their onDisconnect writes queued; let the writers
		// finish them.
		stop, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := a.RTDB.Close(stop); err != nil {
			logger.Warn("rtdb: shutdown", "error", err)
		}
	}
	if a.Storage != nil {
		// Storage waits for its writes in flight and for Run, then releases the data
		// directory's lock, and the next instance may take it (ADR-022 §5.1).
		if err := a.Storage.Close(); err != nil {
			logger.Warn("storage: close", "error", err)
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
