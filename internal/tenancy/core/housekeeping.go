package core

import (
	"context"
	"errors"

	"github.com/arumadev/aruma/internal/platform/retention"
	"github.com/arumadev/aruma/internal/tenancy/queries"
)

// purgeBatch is the rows one DELETE of the control plane's purges removes.
const purgeBatch = 1000

// Retention is how long Housekeep keeps the control plane's tables that grow with use.
type Retention struct {
	// AuditDays keeps control.audit_log this many days; 0 keeps it forever. The database refuses
	// to delete a row younger than 7 days, so a positive value must be at least 7.
	AuditDays int
	// EventsDays keeps events.changes this many days; 0 keeps it forever.
	EventsDays int
}

// Housekeep purges what has stopped working and what has outlived its retention: console
// sessions a day after their absolute expiry, CLI tokens a day after their revocation or 30
// days after their expiry, and the audit log and the shared change log past r. Expired projects,
// apps and service account tokens are PurgeExpired's. It returns the rows purged by table; a
// purge that fails does not stop the others.
func (s *Service) Housekeep(ctx context.Context, r Retention) (retention.Counts, error) {
	c := retention.Counts{}
	errs := []error{
		c.Run(ctx, "control.sessions", purgeBatch, func(ctx context.Context, n int) (int64, error) {
			return s.q.PurgeEndedSessions(ctx, int32(n))
		}),
		c.Run(ctx, "control.access_tokens", purgeBatch, func(ctx context.Context, n int) (int64, error) {
			return s.q.PurgeEndedAccessTokens(ctx, int32(n))
		}),
	}
	if r.AuditDays > 0 {
		errs = append(errs, c.Run(ctx, "control.audit_log", purgeBatch, func(ctx context.Context, n int) (int64, error) {
			return s.q.PurgeAuditLog(ctx, queries.PurgeAuditLogParams{Days: int32(r.AuditDays), Batch: int32(n)})
		}))
	}
	if r.EventsDays > 0 {
		errs = append(errs, c.Run(ctx, "events.changes", purgeBatch, func(ctx context.Context, n int) (int64, error) {
			return s.q.PurgeEvents(ctx, queries.PurgeEventsParams{Days: int32(r.EventsDays), Batch: int32(n)})
		}))
	}
	return c, errors.Join(errs...)
}
