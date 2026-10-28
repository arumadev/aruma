package core

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arumadev/aruma/internal/services/documents/queries"
)

// purgeBatch is the most rows one purge statement deletes.
const purgeBatch = 10_000

// PurgeProject removes every Document Database row of a project whose deletion has expired
// (ADR-024 §1.1): the hot tables, which have no foreign keys, in batches, then its databases,
// whose cascade takes the sequencer, indexes, field overrides and operations. It is idempotent:
// a project without documents costs a few empty deletes. It needs no single-instance lock: the
// project is gone, so no commit reaches its rows.
func PurgeProject(ctx context.Context, pool *pgxpool.Pool, project int64) error {
	q := queries.New(pool)
	tables := []struct {
		name string
		del  func(context.Context) (int64, error)
	}{
		{"documents", func(ctx context.Context) (int64, error) {
			return q.PurgeDocuments(ctx, queries.PurgeDocumentsParams{ProjectNumber: project, Batch: purgeBatch})
		}},
		{"index entries", func(ctx context.Context) (int64, error) {
			return q.PurgeIndexEntries(ctx, queries.PurgeIndexEntriesParams{ProjectNumber: project, Batch: purgeBatch})
		}},
		{"versions", func(ctx context.Context) (int64, error) {
			return q.PurgeVersions(ctx, queries.PurgeVersionsParams{ProjectNumber: project, Batch: purgeBatch})
		}},
		{"changelog", func(ctx context.Context) (int64, error) {
			return q.PurgeChangelog(ctx, queries.PurgeChangelogParams{ProjectNumber: project, Batch: purgeBatch})
		}},
	}
	for _, t := range tables {
		for {
			n, err := deleteBatch(ctx, t.del)
			if err != nil {
				return fmt.Errorf("documents: purge the %s of project %d: %w", t.name, project, err)
			}
			if n < purgeBatch {
				break
			}
		}
	}
	if err := q.PurgeUsage(ctx, project); err != nil {
		return fmt.Errorf("documents: purge the usage of project %d: %w", project, err)
	}
	if err := q.PurgeUsageHours(ctx, project); err != nil {
		return fmt.Errorf("documents: purge the usage hours of project %d: %w", project, err)
	}
	if err := q.PurgeDatabases(ctx, project); err != nil {
		return fmt.Errorf("documents: purge the databases of project %d: %w", project, err)
	}
	return nil
}
