// Package core is Aruma Auth: users, sign-in, tokens and their revocation, behind the
// Identity Toolkit compatible APIs. Errors are *Error values with the codes the Firebase SDKs
// map; everything a client can observe follows the Identity Toolkit API.
package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arumadev/aruma/internal/platform/apierr"
	"github.com/arumadev/aruma/internal/platform/crypto"
	"github.com/arumadev/aruma/internal/platform/idtoken"
	"github.com/arumadev/aruma/internal/platform/retention"
	"github.com/arumadev/aruma/internal/services/auth/queries"
	"github.com/arumadev/aruma/internal/services/auth/tokens"
	tcore "github.com/arumadev/aruma/internal/tenancy/core"
)

// Service is Aruma Auth.
type Service struct {
	pool      *pgxpool.Pool
	q         *queries.Queries
	control   *tcore.Service
	tokens    *tokens.Manager
	keys      *crypto.Keys
	hasher    *crypto.Hasher
	publicURL string
	log       *slog.Logger
	now       func() time.Time
	mail      *mailer
	idp       *idpRuntime

	// The ID token contract with other services (idtoken.go).
	revocations *idtoken.Hub
	ids         projectIDs
	states      stateCache
}

// New returns the Auth service.
func New(pool *pgxpool.Pool, control *tcore.Service, keys *crypto.Keys, hasher *crypto.Hasher, publicURL string, logger *slog.Logger) *Service {
	return &Service{
		pool: pool, q: queries.New(pool), control: control, tokens: tokens.NewManager(pool, keys),
		keys: keys, hasher: hasher, publicURL: strings.TrimSuffix(publicURL, "/"), log: logger, now: time.Now,
		mail: newMailer(),
		idp:  newIdPRuntime(),

		revocations: idtoken.NewHub(idtoken.DefaultBuffer),
	}
}

// Tokens exposes the token manager for the discovery endpoints and housekeeping.
func (s *Service) Tokens() *tokens.Manager { return s.tokens }

// Project identifies the project and tenant a request acts in.
type Project struct {
	Number int64
	ID     string
	Tenant string
}

// Issuer is the ID token issuer of a project (ADR-004).
func (s *Service) Issuer(p Project) string { return s.publicURL + "/issuers/" + p.ID }

// SessionIssuer is the session cookie issuer of a project.
func (s *Service) SessionIssuer(p Project) string { return s.publicURL + "/session-issuers/" + p.ID }

// ProjectForKey resolves a client API key and checks the key's restrictions against the
// caller. A refused caller gets Google's answer: 403 PERMISSION_DENIED with the reason in an
// ErrorInfo detail.
func (s *Service) ProjectForKey(ctx context.Context, key string, caller tcore.KeyCaller) (Project, error) {
	kp, err := s.control.ProjectForKey(ctx, key)
	if err != nil {
		// Only a key that is unknown, disabled or of a deleted project is "not valid"; a
		// database that is down is the server's failure, which clients retry (503), not a
		// 400 that makes the SDK report a bad API key.
		if apierr.CodeOf(err) == apierr.InvalidArgument {
			return Project{}, ErrInvalidAPIKey
		}
		return Project{}, err
	}
	if blocked := kp.Key.Restrictions.Check(caller); blocked != nil {
		md := map[string]string{"consumer": "projects/" + strconv.FormatInt(kp.Number, 10), "service": caller.Service}
		for k, v := range blocked.Metadata {
			md[k] = v
		}
		return Project{}, &Error{Code: blocked.Message, Status: http.StatusForbidden,
			Info: &ErrorInfo{Reason: blocked.Reason, Domain: "googleapis.com", Metadata: md}}
	}
	return Project{Number: kp.Number, ID: kp.ID}, nil
}

// ProjectByNumber resolves a project for the admin APIs, whose caller is already authorised.
func (s *Service) ProjectByNumber(ctx context.Context, number int64) (Project, error) {
	id, err := s.control.ProjectID(ctx, number)
	if err != nil {
		return Project{}, err
	}
	return Project{Number: number, ID: id}, nil
}

// ProjectByID resolves an active project named by id, for the public discovery endpoints.
func (s *Service) ProjectByID(ctx context.Context, id string) (Project, error) {
	n, err := s.control.ProjectNumber(ctx, id)
	if err != nil {
		return Project{}, err
	}
	return Project{Number: n, ID: id}, nil
}

// Config returns the project's Auth settings, created with defaults on first use.
func (s *Service) Config(ctx context.Context, p Project) (queries.AuthProjectConfig, error) {
	c, err := s.q.GetProjectConfig(ctx, p.Number)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := s.q.EnsureProjectConfig(ctx, p.Number); err != nil {
			return c, err
		}
		return s.q.GetProjectConfig(ctx, p.Number)
	}
	return c, err
}

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

// ClientInfo is where a request came from, for the Auth audit log.
type ClientInfo struct {
	IP string
}

func (s *Service) audit(ctx context.Context, q *queries.Queries, p Project, uid, event, result string, details map[string]any) {
	d := json.RawMessage(`{}`)
	if len(details) > 0 {
		d, _ = json.Marshal(details)
	}
	var u *string
	if uid != "" {
		u = &uid
	}
	if err := q.InsertAuthAudit(ctx, queries.InsertAuthAuditParams{
		ProjectNumber: p.Number, TenantID: p.Tenant, Uid: u, Event: event, Result: result, Details: d,
	}); err != nil {
		s.log.Warn("auth audit", "error", err)
	}
}

// Retention is how long Housekeep keeps the Auth tables that grow with use.
type Retention struct {
	// AuditDays keeps auth.audit, a row per sign-in attempt, this many days; 0 keeps it forever.
	AuditDays int
	// Batch is the rows one DELETE removes.
	Batch int
}

// Housekeep rotates and destroys signing keys on schedule, and purges what nothing reads any
// more: refresh tokens 30 days after they stopped working, spent email action codes and Google
// or Apple sign-in sessions, old outbox messages, and audit rows past r.AuditDays. It returns
// the rows purged by table; a purge that fails does not stop the others.
func (s *Service) Housekeep(ctx context.Context, r Retention) (retention.Counts, error) {
	c := retention.Counts{}
	if err := s.tokens.Maintain(ctx); err != nil {
		return c, err
	}
	var errs []error
	run := func(table string, del func(context.Context, int) (int64, error)) {
		errs = append(errs, c.Run(ctx, table, r.Batch, del))
	}
	// One statement each: their rows live minutes to days, so the tables stay small.
	once := func(table string, del func(context.Context) (int64, error)) {
		n, err := del(ctx)
		c[table] += n
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", table, err))
		}
	}
	run("auth.refresh_tokens", func(ctx context.Context, n int) (int64, error) {
		return s.q.PurgeEndedRefreshTokens(ctx, int32(n))
	})
	once("auth.idp_sessions", s.q.PurgeIdPSessions)
	once("auth.oob_codes", s.q.PurgeOobCodes)
	once("auth.email_outbox", s.q.PurgeOutbox)
	if r.AuditDays > 0 {
		run("auth.audit", func(ctx context.Context, n int) (int64, error) {
			return s.q.PurgeAuthAudit(ctx, queries.PurgeAuthAuditParams{Days: int32(r.AuditDays), Batch: int32(n)})
		})
	}
	return c, errors.Join(errs...)
}

// projectPurgeBatch is the rows one statement of PurgeProject deletes.
const projectPurgeBatch = 1000

// PurgeProject deletes a purged project's Auth data: its users and everything they own, its
// sessions, codes, mail, keys, settings and audit rows. Tenancy runs it at each housekeeping
// pass for every project whose deletion has expired (OnProjectPurged); a large project goes over
// several passes, retention.MaxBatches batches of each table at a time, and a project with
// nothing left costs a few small statements that delete nothing.
func (s *Service) PurgeProject(ctx context.Context, project int64) error {
	c := retention.Counts{}
	q := s.q
	batched := []struct {
		table string
		del   func(ctx context.Context, b int32) (int64, error)
	}{
		{"auth.refresh_tokens", func(ctx context.Context, b int32) (int64, error) {
			return q.PurgeProjectRefreshTokens(ctx, queries.PurgeProjectRefreshTokensParams{ProjectNumber: project, Batch: b})
		}},
		{"auth.users", func(ctx context.Context, b int32) (int64, error) {
			return q.PurgeProjectUsers(ctx, queries.PurgeProjectUsersParams{ProjectNumber: project, Batch: b})
		}},
		{"auth.audit", func(ctx context.Context, b int32) (int64, error) {
			return q.PurgeProjectAuthAudit(ctx, queries.PurgeProjectAuthAuditParams{ProjectNumber: project, Batch: b})
		}},
		{"auth.email_outbox", func(ctx context.Context, b int32) (int64, error) {
			return q.PurgeProjectOutbox(ctx, queries.PurgeProjectOutboxParams{ProjectNumber: project, Batch: b})
		}},
	}
	for _, t := range batched {
		if err := c.Run(ctx, t.table, projectPurgeBatch, func(ctx context.Context, n int) (int64, error) {
			return t.del(ctx, int32(n))
		}); err != nil {
			return err
		}
	}
	for _, del := range []func(context.Context, int64) error{
		s.q.PurgeProjectOobCodes, s.q.PurgeProjectIdPSessions, s.q.PurgeProjectIdPConfigs,
		s.q.PurgeProjectSigningKeys, s.q.PurgeProjectEmailTemplates, s.q.PurgeProjectAuthConfig,
	} {
		if err := del(ctx, project); err != nil {
			return err
		}
	}
	if n := c.Total(); n > 0 {
		s.log.Info("purged an expired project's auth data", "project_number", project, "rows", n)
	}
	return nil
}
