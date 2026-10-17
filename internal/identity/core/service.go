// Package core is Aruma Auth: users, sign-in, tokens and their revocation, behind the
// Identity Toolkit compatible APIs. Errors are *Error values with the codes the Firebase SDKs
// map; everything a client can observe follows docs/research/phase2-identity-toolkit.md.
package core

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/osama300/aurma-project/internal/identity/queries"
	"github.com/osama300/aurma-project/internal/identity/tokens"
	"github.com/osama300/aurma-project/internal/platform/crypto"
	tcore "github.com/osama300/aurma-project/internal/tenancy/core"
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
}

// New returns the Auth service.
func New(pool *pgxpool.Pool, control *tcore.Service, keys *crypto.Keys, hasher *crypto.Hasher, publicURL string, logger *slog.Logger) *Service {
	return &Service{
		pool: pool, q: queries.New(pool), control: control, tokens: tokens.NewManager(pool, keys),
		keys: keys, hasher: hasher, publicURL: strings.TrimSuffix(publicURL, "/"), log: logger, now: time.Now,
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

// ProjectForKey resolves a client API key.
func (s *Service) ProjectForKey(ctx context.Context, key string) (Project, error) {
	kp, err := s.control.ProjectForKey(ctx, key)
	if err != nil {
		return Project{}, ErrInvalidAPIKey
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

// Housekeep rotates and destroys signing keys on schedule and purges refresh tokens that can
// no longer be used.
func (s *Service) Housekeep(ctx context.Context) error {
	if err := s.tokens.Maintain(ctx); err != nil {
		return err
	}
	n, err := s.q.PurgeOrphanRefreshTokens(ctx)
	if n > 0 {
		s.log.Info("auth housekeeping", "refresh_tokens_purged", n)
	}
	return err
}
