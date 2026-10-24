package core

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/platform/httpx"
	"github.com/arumadev/aruma/internal/tenancy/queries"
)

// audit records an administrative action on a project within the caller's transaction, so the
// entry exists exactly when the action took effect. details must never hold secrets.
func (s *Service) audit(ctx context.Context, q *queries.Queries, p *authn.Principal, project int64, action, target string, details map[string]any) error {
	actorID := p.ActorID()
	params := queries.InsertAuditParams{
		ActorType:     p.ActorType(),
		ActorID:       &actorID,
		ProjectNumber: &project,
		Action:        action,
		Target:        &target,
		Result:        "ok",
		Details:       json.RawMessage(`{}`),
	}
	if ip := httpx.ClientAddr(ctx); ip.IsValid() {
		params.Ip = &ip
	}
	if len(details) > 0 {
		b, err := json.Marshal(details)
		if err != nil {
			return err
		}
		params.Details = b
	}
	return q.InsertAudit(ctx, params)
}

// AuditEntry is one line of a project's audit log.
type AuditEntry struct {
	ID        int64
	At        time.Time
	ActorType string
	ActorID   *uuid.UUID
	Action    string
	Target    string
	Result    string
	IP        string
	Details   json.RawMessage
}

// ListAudit returns a project's audit log, newest first.
func (s *Service) ListAudit(ctx context.Context, p *authn.Principal, ref string, opt ListOptions) (Page[AuditEntry], error) {
	proj, err := s.resolveProject(ctx, s.q, p, ref, true)
	if err != nil {
		return Page[AuditEntry]{}, err
	}
	before := int64(1<<63 - 1)
	if opt.PageToken != "" {
		raw, err := decodeToken(opt.PageToken)
		if err != nil {
			return Page[AuditEntry]{}, err
		}
		if before, err = strconv.ParseInt(raw, 10, 64); err != nil {
			return Page[AuditEntry]{}, errBadPageToken
		}
	}
	size := pageSize(opt.PageSize)
	rows, err := s.q.ListAudit(ctx, queries.ListAuditParams{ProjectNumber: &proj.ProjectNumber, BeforeID: before, PageSize: size + 1})
	if err != nil {
		return Page[AuditEntry]{}, internal(err)
	}
	var page Page[AuditEntry]
	if len(rows) > int(size) {
		rows = rows[:size]
		page.NextPageToken = encodeToken(strconv.FormatInt(rows[len(rows)-1].ID, 10))
	}
	page.Items = make([]AuditEntry, 0, len(rows))
	for _, r := range rows {
		e := AuditEntry{ID: r.ID, At: r.At, ActorType: r.ActorType, ActorID: r.ActorID, Action: r.Action, Result: r.Result, Details: r.Details}
		if r.Target != nil {
			e.Target = *r.Target
		}
		if r.Ip != nil {
			e.IP = r.Ip.String()
		}
		page.Items = append(page.Items, e)
	}
	return page, nil
}

// Audit records an administrative action a data service took on a project, once it took
// effect: Storage's dashboard writes and the upload sessions the dashboard issues (ADR-022 §15).
// details must never hold secrets.
func (s *Service) Audit(ctx context.Context, p *authn.Principal, project int64, action, target string, details map[string]any) error {
	if err := s.audit(ctx, s.q, p, project, action, target, details); err != nil {
		return internal(err)
	}
	return nil
}
