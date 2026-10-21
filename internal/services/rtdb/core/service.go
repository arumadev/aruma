// Package core is Aruma's Realtime Database apart from any transport: the data model and its
// orders and hash (node.go, order.go, hash.go), writes and server values, the single writer
// per database (database.go) on Postgres storage (store_pg.go), and sessions. The WebSocket
// protocol lives in package wire and the endpoints in package api; errors carry both the
// protocol status and the REST code (errors.go).
package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/arumadev/aruma/internal/platform/apierr"
	"github.com/arumadev/aruma/internal/platform/idtoken"
	"github.com/arumadev/aruma/internal/services/rtdb/rules"
)

// Projects resolves project ids; the control plane implements it.
type Projects interface {
	// ProjectNumber returns the number of an active project; an apierr NOT_FOUND when there
	// is none, any other error when it cannot tell.
	ProjectNumber(ctx context.Context, id string) (int64, error)
}

// Options configure the service.
type Options struct {
	Limits Limits
	// DevOpenRules acts as the test-mode rules {".read": true, ".write": true} on every
	// database, for sessions opened with OpenOptions.Loopback, instead of the database's own
	// rules. Off, every client answers to its database's rules, which start locked (ADR-020).
	DevOpenRules bool
	// IDTokens verifies the ID tokens of Aruma Auth users who sign in (Session.SignIn); nil
	// refuses them. Revocations announces the users whose tokens stop being valid, so their
	// sessions end (revoke.go); nil leaves them to their tokens' expiry.
	IDTokens    idtoken.Verifier
	Revocations idtoken.Revocations
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
	store     Store
	registry  *registry
	conns     *counter
	// idTokens verifies users' ID tokens; revocations ends their sessions (revoke.go).
	idTokens    idtoken.Verifier
	revocations *revocations
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
	svc := &Service{
		projects: projects, limits: opts.Limits, openRules: opts.DevOpenRules, now: opts.Now, log: logger,
		store:    store,
		registry: newRegistry(store, opts.Now, logger, opts.Limits, opts.IdleTimeout),
		conns:    &counter{limit: opts.Limits.ConnectionsPerProject, n: map[int64]int{}},
		idTokens: opts.IDTokens,
	}
	src := opts.Revocations
	if opts.IDTokens == nil {
		src = nil // without a verifier no user signs in, and nothing needs watching
	}
	svc.revocations = newRevocations(src, opts.IDTokens, logger)
	return svc
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
	var ae *apierr.Error
	switch {
	case errors.As(err, &ae) && ae.Code == apierr.NotFound:
		return Target{}, ErrNoDatabase
	case err != nil:
		// The control plane could not answer: the database may well exist.
		s.log.Error("rtdb: resolve namespace", "namespace", ns, "error", err)
		return Target{}, ErrUnavailable
	}
	return Target{Name: ns, ProjectID: id, Project: n}, nil
}

// ErrTooManyConnections is a project at its connection limit.
var ErrTooManyConnections = &Error{statusUnavailable, "Too many connections to this project's database.", http.StatusTooManyRequests}

// connect counts a session against its project's limit. The returned release must be called
// when the session ends.
func (s *Service) connect(t Target) (release func(), err error) {
	if !s.conns.acquire(t.Project) {
		return nil, ErrTooManyConnections
	}
	return func() { s.conns.release(t.Project) }, nil
}

// Open starts a session on a database as auth. Close it when the client goes.
func (s *Service) Open(t Target, auth Auth, o OpenOptions) (*Session, error) {
	db, err := s.registry.acquire(t.Project, t.Name)
	if err != nil {
		return nil, err
	}
	sess := &Session{svc: s, db: db, target: t, loopback: o.Loopback, sink: o.Sink}
	if err := sess.SetAuth(auth); err != nil {
		s.registry.release(db)
		return nil, err
	}
	return sess, nil
}

// maxRulesBytes caps a rules document.
const maxRulesBytes = 256 << 10

// Rules returns a database's rules document as stored.
func (s *Service) Rules(ctx context.Context, t Target) (string, error) {
	info, err := s.store.OpenDatabase(ctx, t.Project, t.Name)
	if err != nil {
		s.log.Error("rtdb: read rules", "database", t.Name, "error", err)
		return "", ErrUnavailable
	}
	return info.Rules, nil
}

// CheckRules checks a rules document as SetRules would, and saves nothing (dryRun).
func (s *Service) CheckRules(doc string) error {
	_, err := parseRules(doc)
	return err
}

func parseRules(doc string) (*rules.Ruleset, error) {
	if len(doc) > maxRulesBytes {
		return nil, ErrTooBig
	}
	rs, err := rules.Parse([]byte(doc))
	if err != nil {
		return nil, &Error{statusInvalidParameters, rulesErrorText(err), http.StatusBadRequest}
	}
	return rs, nil
}

// rulesErrorText is a rules load error as Firebase prints it: each error "line:col: message"
// on a line of its own, without the rule's path (emulator 4.11.2, golden rest.golden.json).
// The console shows the paths; it parses the document itself.
func rulesErrorText(err error) string {
	var es rules.Errors
	var e *rules.Error
	switch {
	case errors.As(err, &es):
	case errors.As(err, &e):
		es = rules.Errors{e}
	default:
		return err.Error()
	}
	var b strings.Builder
	for _, e := range es {
		fmt.Fprintf(&b, "%d:%d: %s\n", e.Line, e.Column, e.Msg)
	}
	return b.String()
}

// SetRules replaces a database's rules document, once it parses. The database's writer takes
// the new rules in its order: commands queued before see the old ones, and every listen open
// then is checked against the new ones and cancelled with c if they no longer permit it
// (Database.recheckListens). Their .indexOn takes effect at once too.
func (s *Service) SetRules(ctx context.Context, t Target, doc string) error {
	rs, err := parseRules(doc)
	if err != nil {
		return err
	}
	if _, err := s.store.OpenDatabase(ctx, t.Project, t.Name); err != nil {
		s.log.Error("rtdb: save rules", "database", t.Name, "error", err)
		return ErrUnavailable
	}
	if err := s.store.SetRules(ctx, t.Project, t.Name, doc); err != nil {
		s.log.Error("rtdb: save rules", "database", t.Name, "error", err)
		return ErrUnavailable
	}
	db, err := s.registry.acquire(t.Project, t.Name)
	if err != nil {
		return err
	}
	defer s.registry.release(db)
	done := make(chan struct{})
	db.submit(&command{rules: rs, done: func(Result) { close(done) }})
	<-done
	return nil
}

// PurgeProject deletes every database of a project, for the control plane's purge of a
// project whose deletion has expired.
func (s *Service) PurgeProject(ctx context.Context, project int64) error {
	return s.store.DeleteProject(ctx, project)
}

// Close stops the writers once their sessions have closed, waiting for queued writes until
// ctx ends. Close the transports, and so the sessions, first.
func (s *Service) Close(ctx context.Context) error { return s.registry.close(ctx) }
