package core

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5"

	"github.com/arumadev/aruma/internal/services/storage/queries"
)

// Quota and disk (ADR-022 §6.5). A project's quota covers its objects and its staged bytes, and
// is reserved for bytes as they arrive: a chunk, an 8 MiB step of a body of unknown length, an
// Insert or a Rewrite. Every reservation lives in storage.usage, so the database is the one
// ledger; a session also holds its part in its own row, and Boot rebuilds the accounts from the
// rows after a crash. An upload's start reserves nothing.
//
// The disk is the process's, not a project's: every reservation step also takes its bytes
// against min_free_bytes from one in-flight counter, checked and added in one atomic step, so
// two requests never pass one check together.

// reserveStep is what a body of unknown length reserves at a time; the rest goes back when the
// request ends. Near the quota, a step that does not fit is halved down to minReserveStep before
// the request is refused, so a small body of unknown length is not refused for the 8 MiB it
// would never have used.
const (
	reserveStep    = 8 << 20
	minReserveStep = 64 << 10
)

// Usage returns a project's storage use and quota.
func (s *Service) Usage(ctx context.Context, b Bucket) (Usage, error) {
	u, err := s.reads.GetUsage(ctx, b.Project)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Usage{QuotaBytes: s.limits.ProjectBytes}, nil
	case err != nil:
		return Usage{}, fmt.Errorf("%w: read usage: %w", ErrUnavailable, err)
	}
	return Usage{UsedBytes: u.UsedBytes, ReservedBytes: u.ReservedBytes, Objects: u.ObjectCount,
		QuotaBytes: s.limits.ProjectBytes}, nil
}

// anonymousCap is the most a project's sessions without an identity may hold staged:
// anonymous_staged_fraction of project_bytes, unlimited with the quota.
func (s *Service) anonymousCap() int64 {
	if s.limits.ProjectBytes == 0 {
		return math.MaxInt64
	}
	return int64(float64(s.limits.ProjectBytes) * s.limits.AnonymousStagedFraction)
}

// reserve takes n bytes of a project's quota in the caller's transaction, anonymously for a
// session without an identity. Over the quota or the anonymous cap it is ErrQuotaExceeded.
func (s *Service) reserve(ctx context.Context, q *queries.Queries, project, n int64, anonymous bool) error {
	if n <= 0 {
		return nil
	}
	if err := q.EnsureUsage(ctx, project); err != nil {
		return fmt.Errorf("%w: usage: %w", ErrUnavailable, err)
	}
	var anon int64
	if anonymous {
		anon = n
	}
	rows, err := q.ReserveUsage(ctx, queries.ReserveUsageParams{N: n, AnonymousN: anon, ProjectNumber: project,
		Quota: s.limits.ProjectBytes, AnonymousCap: s.anonymousCap()})
	if err != nil {
		return fmt.Errorf("%w: reserve: %w", ErrUnavailable, err)
	}
	if rows == 0 {
		reason := "quota"
		if u, err := q.GetUsage(ctx, project); err == nil && anonymous && u.AnonymousReservedBytes+n > s.anonymousCap() {
			reason = "anonymous_quota"
		}
		return &quotaRefusal{reason: reason}
	}
	return nil
}

// quotaRefusal is ErrQuotaExceeded with its reason, which the request counts once it gives up.
type quotaRefusal struct{ reason string }

func (e *quotaRefusal) Error() string { return ErrQuotaExceeded.Error() }
func (e *quotaRefusal) Unwrap() error { return ErrQuotaExceeded }

// countRefusal counts a write refused for the quota, by its reason.
func (s *Service) countRefusal(err error) {
	var q *quotaRefusal
	if errors.As(err, &q) {
		s.metrics.Rejections.WithLabelValues(q.reason).Inc()
	}
}

// unreserve gives n reserved bytes back in the caller's transaction.
func unreserve(ctx context.Context, q *queries.Queries, project, n int64, anonymous bool) error {
	if n <= 0 {
		return nil
	}
	var anon int64
	if anonymous {
		anon = n
	}
	if err := q.UnreserveUsage(ctx, queries.UnreserveUsageParams{N: n, AnonymousN: anon, ProjectNumber: project}); err != nil {
		return fmt.Errorf("%w: unreserve: %w", ErrUnavailable, err)
	}
	return nil
}

// fitsQuota is an upload start's early check: a declared size must fit what is left of the
// quota, and of the anonymous share for a session without an identity. It reserves nothing.
func (s *Service) fitsQuota(ctx context.Context, project, declared int64, anonymous bool) error {
	if declared <= 0 || s.limits.ProjectBytes == 0 {
		return nil
	}
	u, err := s.reads.GetUsage(ctx, project)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		u = queries.StorageUsage{}
	case err != nil:
		return fmt.Errorf("%w: read usage: %w", ErrUnavailable, err)
	}
	if declared > s.limits.ProjectBytes-u.UsedBytes-u.ReservedBytes {
		s.metrics.Rejections.WithLabelValues("quota").Inc()
		return ErrQuotaExceeded
	}
	if anonymous && declared > s.anonymousCap()-u.AnonymousReservedBytes {
		s.metrics.Rejections.WithLabelValues("anonymous_quota").Inc()
		return ErrQuotaExceeded
	}
	return nil
}

// reserveDisk takes n bytes against min_free_bytes: free − inflight − n ≥ min_free, checked and
// added in one compare-and-swap, so concurrent requests cannot pass one check together. Below the
// floor it is ErrUnavailable (503: the disk is shared with Postgres's WAL). The caller gives the
// bytes back with releaseDisk once they are written, when the free space shows them.
func (s *Service) reserveDisk(n int64) error {
	if n <= 0 {
		return nil
	}
	free, err := s.store.FreeBytes()
	if err != nil {
		return fmt.Errorf("%w: free space: %w", ErrUnavailable, err)
	}
	for {
		cur := s.diskInflight.Load()
		if free-cur-n < s.limits.MinFreeBytes {
			s.metrics.Rejections.WithLabelValues("disk").Inc()
			return fmt.Errorf("%w: the disk is below min_free_bytes", ErrUnavailable)
		}
		if s.diskInflight.CompareAndSwap(cur, cur+n) {
			return nil
		}
	}
}

func (s *Service) releaseDisk(n int64) {
	if n > 0 {
		s.diskInflight.Add(-n)
	}
}
