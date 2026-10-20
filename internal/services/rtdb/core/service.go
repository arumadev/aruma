// Package core is Aruma's Realtime Database apart from any transport: the data model and its
// orders and hash (node.go, order.go, hash.go), writes and server values, the single writer
// per database (database.go) on Postgres storage (store_pg.go), and sessions. The WebSocket
// protocol lives in package wire and the endpoints in package api; errors carry both the
// protocol status and the REST code (errors.go).
package core

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"
)

// Projects resolves project ids; the control plane implements it.
type Projects interface {
	// ProjectNumber returns the number of an active project, or an error.
	ProjectNumber(ctx context.Context, id string) (int64, error)
}

// Options configure the service.
type Options struct {
	Limits Limits
	// DevOpenRules acts as the test-mode rules {".read": true, ".write": true} on every
	// database. Off, databases keep the locked rules they start with (ADR-020).
	DevOpenRules bool
	// IdleTimeout is how long a database's writer outlives its last session; 0 means 5 minutes.
	IdleTimeout time.Duration
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Service is the Realtime Database.
type Service struct {
	projects  Projects
	limits    Limits
	openRules bool
	now       func() time.Time
	log       *slog.Logger
	registry  *registry
	conns     *counter
}

// New returns the service.
func New(store Store, projects Projects, opts Options, logger *slog.Logger) *Service {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.IdleTimeout <= 0 {
		opts.IdleTimeout = 5 * time.Minute
	}
	if opts.Limits == (Limits{}) {
		opts.Limits = DefaultLimits
	}
	return &Service{
		projects: projects, limits: opts.Limits, openRules: opts.DevOpenRules, now: opts.Now, log: logger,
		registry: newRegistry(store, opts.Now, logger, opts.Limits, opts.IdleTimeout),
		conns:    &counter{limit: opts.Limits.ConnectionsPerProject, n: map[int64]int{}},
	}
}

// Limits returns the configured limits.
func (s *Service) Limits() Limits { return s.limits }

// Now is the server time.
func (s *Service) Now() time.Time { return s.now() }

// OnCommit installs the hook that receives every database's commits, after the commit and
// before the replies (the fan-out, R3). Set it before the first session opens.
func (s *Service) OnCommit(fn func(db *Database, c Commit)) { s.registry.onCommit = fn }

// DatabaseSuffix ends the name of a project's default database (ADR-018).
const DatabaseSuffix = "-default-rtdb"

// Target is a resolved namespace: a database of an active project.
type Target struct {
	Name      string // the namespace, <project_id>-default-rtdb
	ProjectID string
	Project   int64
}

// Resolve checks that ns names the database of an active project. Only the default database
// exists in v0.2. Names starting with s- are reserved: the SDKs cache such hosts as the
// server's own shards (RepoInfo.ts:62-84).
func (s *Service) Resolve(ctx context.Context, ns string) (Target, error) {
	id, ok := strings.CutSuffix(ns, DatabaseSuffix)
	if !ok || id == "" || strings.HasPrefix(ns, "s-") || ns != strings.ToLower(ns) {
		return Target{}, ErrNoDatabase
	}
	n, err := s.projects.ProjectNumber(ctx, id)
	if err != nil {
		return Target{}, ErrNoDatabase
	}
	return Target{Name: ns, ProjectID: id, Project: n}, nil
}

// ErrTooManyConnections is a project at its connection limit.
var ErrTooManyConnections = errors.New("rtdb: too many connections for this project")

// Connect counts a connection against its project's limit. The returned release must be
// called when the connection ends.
func (s *Service) Connect(t Target) (release func(), err error) {
	if !s.conns.acquire(t.Project) {
		return nil, ErrTooManyConnections
	}
	return func() { s.conns.release(t.Project) }, nil
}

// Open starts a session on a database as auth. Close it when the client goes.
func (s *Service) Open(t Target, auth Auth) (*Session, error) {
	db, err := s.registry.acquire(t.Project, t.Name)
	if err != nil {
		return nil, err
	}
	return &Session{svc: s, db: db, auth: auth}, nil
}

// Close stops the writers once their sessions have closed, waiting for queued writes until
// ctx ends. Close the transports, and so the sessions, first.
func (s *Service) Close(ctx context.Context) error { return s.registry.close(ctx) }
