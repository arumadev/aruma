package core

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/arumadev/aruma/internal/services/storage/queries"
)

// Storage's transactions. The write order relies on a commit being durable when it returns
// (ADR-022 §5), so every Storage transaction (an upload's start, the update of received, the
// blob_key written before a promote, commitObject, a delete, the GC and the sweep) begins
// through begin or inTx, which set synchronous_commit for that transaction alone. The pool keeps
// the server's default for every other service, and no startup parameter reaches a pooler such
// as PgBouncer, which refuses one it does not know.

// begin starts a Storage transaction that commits synchronously. commitObject uses it directly,
// to read the transaction's xid before COMMIT; everything else goes through inTx.
func (s *Service) begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: begin: %w", ErrUnavailable, err)
	}
	if _, err := tx.Exec(ctx, "SET LOCAL synchronous_commit TO on"); err != nil {
		_ = tx.Rollback(context.WithoutCancel(ctx))
		return nil, fmt.Errorf("%w: begin: %w", ErrUnavailable, err)
	}
	return tx, nil
}

// inTx runs fn in a Storage transaction, committing when it returns nil. The COMMIT is sent
// under context.WithoutCancel: a client that drops its connection cancels the request, never a
// commit in flight (ADR-022 §5).
func (s *Service) inTx(ctx context.Context, fn func(q *queries.Queries) error) (err error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()
	if err = fn(queries.New(tx)); err != nil {
		return err
	}
	return tx.Commit(context.WithoutCancel(ctx))
}

// warnUnsafeDurability logs a warning when the Postgres server runs with fsync off, which no
// session can change and which "can result in unrecoverable data corruption" (ADR-022 §5).
// Boot calls it.
func (s *Service) warnUnsafeDurability(ctx context.Context) error {
	var fsync string
	if err := s.pool.QueryRow(ctx, "SHOW fsync").Scan(&fsync); err != nil {
		return fmt.Errorf("%w: read fsync: %w", ErrUnavailable, err)
	}
	if fsync != "on" {
		s.log.Warn("storage: Postgres runs with fsync off; a crash may lose uploads that were confirmed, or corrupt the database. Set fsync = on.")
	}
	return nil
}

// releaseObject takes an object generation out of its project, in the caller's transaction: its
// bytes and count leave the quota, and its file goes to blob_gc. Delete (S2-C) calls it for the
// object it removes, commitObject (S2-D) for the generation an overwrite replaces, so the two
// cannot account differently.
func releaseObject(ctx context.Context, q *queries.Queries, project int64, blobKey string, size int64) error {
	if _, err := q.ReleaseUsage(ctx, queries.ReleaseUsageParams{ProjectNumber: project, Size: size}); err != nil {
		// A missing usage row is not an error: Boot's reconciliation rebuilds it.
		return fmt.Errorf("%w: release usage: %w", ErrUnavailable, err)
	}
	if err := q.QueueBlobGC(ctx, queries.QueueBlobGCParams{BlobKey: blobKey, ProjectNumber: project}); err != nil {
		return fmt.Errorf("%w: queue %s: %w", ErrUnavailable, blobKey, err)
	}
	return nil
}
