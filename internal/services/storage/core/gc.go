package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/arumadev/aruma/internal/services/storage/queries"
)

// The background work on rows (ADR-022 §5, §6.5): the blob_gc worker, the expiry of sessions by
// their age, and the idle release. Run calls them on every round; each takes a bounded batch, and
// a session a request is working on is passed over and found on a later round.

// gcBatch is how many rows one round takes of each kind of work.
const gcBatch = 500

// collectGarbage deletes the files blob_gc has held for gc_grace, then their rows. The grace
// covers a reader between an object's row and its file; a file a reader holds open stays
// readable on POSIX, and on Windows its delete fails and is tried again later.
func (s *Service) collectGarbage(ctx context.Context) (int, error) {
	due, err := s.reads.ListDueBlobGC(ctx, queries.ListDueBlobGCParams{Before: s.now().Add(-s.gcGrace), N: gcBatch})
	if err != nil {
		return 0, fmt.Errorf("%w: list blob_gc: %w", ErrUnavailable, err)
	}
	var deleted []string
	for _, d := range due {
		if err := s.store.Delete(d.BlobKey); err != nil {
			s.log.Warn("storage: gc: delete a file; it is tried again later", "key", d.BlobKey, "error", err)
			continue
		}
		deleted = append(deleted, d.BlobKey)
	}
	if len(deleted) == 0 {
		return 0, nil
	}
	if err := s.inTx(ctx, func(q *queries.Queries) error { return q.DeleteBlobGC(ctx, deleted) }); err != nil {
		// The files are gone; the rows go on the next round, and deleting a file twice is nothing.
		return 0, fmt.Errorf("%w: clear blob_gc: %w", ErrUnavailable, err)
	}
	return len(deleted), nil
}

// expireSessions ends the sessions whose expires_at has passed: an empty session after
// empty_session_ttl, any other after its life, and a final one once its outcome need not be
// repeated any more.
func (s *Service) expireSessions(ctx context.Context) (int, error) {
	rows, err := s.reads.ListExpiredUploads(ctx, queries.ListExpiredUploadsParams{Now: s.now(), N: gcBatch})
	if err != nil {
		return 0, fmt.Errorf("%w: list expired sessions: %w", ErrUnavailable, err)
	}
	n := 0
	for _, r := range rows {
		unlock, ok := s.sessions.tryLock(r.IDHash)
		if !ok {
			continue
		}
		err := s.expireSession(ctx, r.ProjectNumber, r.IDHash)
		unlock()
		if err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// expireSession deletes a session whose time is up, with its lock held: its reservation goes
// back, and its staging file and an object key it never committed go to blob_gc. It reads the
// session again first: a durable byte may have moved expires_at since it was listed.
func (s *Service) expireSession(ctx context.Context, project int64, idHash []byte) error {
	ctx = context.WithoutCancel(ctx)
	err := s.inTx(ctx, func(q *queries.Queries) error {
		cur, err := q.GetUploadSession(ctx, queries.GetUploadSessionParams{IDHash: idHash, ProjectNumber: project})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if s.now().Before(cur.ExpiresAt) {
			return nil
		}
		if err := s.dropSessionFiles(ctx, q, cur); err != nil {
			return err
		}
		return q.DeleteUploadSession(ctx, queries.DeleteUploadSessionParams{IDHash: idHash, ProjectNumber: project})
	})
	if err != nil {
		return fmt.Errorf("%w: expire a session: %w", ErrUnavailable, err)
	}
	return nil
}

// releaseIdle gives back what idle sessions hold beyond their durable bytes: a session without
// a durable byte for upload_idle_ttl keeps its stored bytes counted while it lives, and reserves
// again with its next chunk, so Android can still resume hours later. One that received nothing
// ends.
func (s *Service) releaseIdle(ctx context.Context) (int, error) {
	before := s.now().Add(-s.limits.UploadIdleTTL)
	rows, err := s.reads.ListIdleUploads(ctx, queries.ListIdleUploadsParams{Before: before, N: gcBatch})
	if err != nil {
		return 0, fmt.Errorf("%w: list idle sessions: %w", ErrUnavailable, err)
	}
	n := 0
	for _, r := range rows {
		unlock, ok := s.sessions.tryLock(r.IDHash)
		if !ok {
			continue
		}
		err := s.inTx(context.WithoutCancel(ctx), func(q *queries.Queries) error {
			cur, err := q.GetUploadSession(ctx, queries.GetUploadSessionParams{IDHash: r.IDHash, ProjectNumber: r.ProjectNumber})
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			if cur.State != "active" || cur.ProgressedAt.After(before) {
				return nil
			}
			if cur.Received == 0 {
				if err := s.dropSessionFiles(ctx, q, cur); err != nil {
					return err
				}
				return q.DeleteUploadSession(ctx, queries.DeleteUploadSessionParams{IDHash: cur.IDHash, ProjectNumber: cur.ProjectNumber})
			}
			if _, err := q.ReleaseIdleUpload(ctx, queries.ReleaseIdleUploadParams{IDHash: cur.IDHash, ProjectNumber: cur.ProjectNumber}); err != nil {
				return err
			}
			return unreserve(ctx, q, cur.ProjectNumber, cur.ReservedBytes-cur.Received, cur.PrincipalKind == kindAnonymous)
		})
		unlock()
		if err != nil {
			return n, fmt.Errorf("%w: release an idle session: %w", ErrUnavailable, err)
		}
		n++
	}
	return n, nil
}

// reportGauges sets the gauges of the live sessions, their staged bytes and the outbox.
func (s *Service) reportGauges(ctx context.Context) error {
	u, err := s.reads.UploadsSummary(ctx)
	if err != nil {
		return fmt.Errorf("%w: count the sessions: %w", ErrUnavailable, err)
	}
	s.metrics.Sessions.Set(float64(u.Sessions))
	s.metrics.StagingBytes.Set(float64(u.Staged))
	now := s.now()
	g, err := s.reads.BlobGCStats(ctx, now)
	if err != nil {
		return fmt.Errorf("%w: count blob_gc: %w", ErrUnavailable, err)
	}
	s.metrics.GCQueue.Set(float64(g.Queued))
	s.metrics.GCOldestSeconds.Set(max(now.Sub(g.Oldest).Seconds(), 0))
	return nil
}
