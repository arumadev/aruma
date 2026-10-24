// Package core is the business logic of the control plane: accounts, sessions, projects,
// apps, API keys and service accounts. Every API (console, Management API, operator CLI) goes
// through it, so ownership checks and validation live in one place.
//
// Methods return *apierr.Error values the transports render unchanged.
package core

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arumadev/aruma/internal/platform/apierr"
	"github.com/arumadev/aruma/internal/platform/crypto"
	"github.com/arumadev/aruma/internal/platform/rules"
	"github.com/arumadev/aruma/internal/tenancy/queries"
)

// Options configure the service.
type Options struct {
	SignupOpen      bool
	IdleTimeout     time.Duration
	AbsoluteTimeout time.Duration
	// PublicURL is the server's address without a trailing slash, e.g. https://aruma.example.com.
	PublicURL string
	// ProjectDomain is the base domain for auth domains, buckets and service account emails.
	ProjectDomain string
	// RTDBDomain is rtdb.domain: the parent of the Realtime Database hosts (ADR-018). Empty,
	// databaseURL takes the development form on PublicURL.
	RTDBDomain string
	// RulesLimits compile the rulesets saved (CreateRuleset) and bound the cache of compiled
	// ones (CompiledRelease, compiled_rulesets_bytes) (ADR-021 §4, §7).
	RulesLimits rules.Limits
}

// Service is the control plane.
type Service struct {
	pool   *pgxpool.Pool
	q      *queries.Queries
	keys   *crypto.Keys
	hasher *crypto.Hasher
	opts   Options
	log    *slog.Logger

	setupMu   sync.Mutex
	setupHash []byte // HMAC of the one-time setup code; nil once used or when not needed

	// purgeHooks remove the data services' data of a purged project (OnProjectPurged). Set
	// while wiring, before the server runs.
	purgeHooks []func(ctx context.Context, project int64) error

	// compiled caches the compiled rulesets of the releases (rules.go), made on first use.
	compiledOnce sync.Once
	compiled     *releaseCache
	// compiles and loads bound the rules compilations running at once in the process: the
	// management's (CompileRules), and the loads of CompiledRelease (maxCompiles).
	compiles chan struct{}
	loads    chan struct{}
}

// New returns the service.
func New(pool *pgxpool.Pool, keys *crypto.Keys, hasher *crypto.Hasher, opts Options, logger *slog.Logger) *Service {
	return &Service{pool: pool, q: queries.New(pool), keys: keys, hasher: hasher, opts: opts, log: logger,
		compiles: make(chan struct{}, maxCompiles()), loads: make(chan struct{}, maxCompiles())}
}

// Options returns the service configuration.
func (s *Service) Options() Options { return s.opts }

// Keys returns the derived key set, for transports that need CSRF tokens.
func (s *Service) Keys() *crypto.Keys { return s.keys }

// tx runs fn in a transaction with query helpers bound to it.
func (s *Service) tx(ctx context.Context, fn func(q *queries.Queries, tx pgx.Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := fn(s.q.WithTx(tx), tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func notFound(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// errNotFound is the uniform answer for anything missing or not owned by the caller, so the
// existence of other people's resources is never revealed (ADR-010 S-11).
func errNotFound() *apierr.Error {
	return apierr.New(apierr.NotFound, "Requested entity was not found.")
}

// internal wraps an unexpected error; its cause is logged by the transport, never shown.
func internal(err error) error {
	if err == nil {
		return nil
	}
	var e *apierr.Error
	if errors.As(err, &e) {
		return e
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return apierr.Wrap(err, apierr.Canceled, "The request was cancelled.")
	}
	return apierr.Wrap(err, apierr.Internal, "Internal error encountered.")
}
