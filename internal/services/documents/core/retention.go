package core

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/arumadev/aruma/internal/services/documents/queries"
)

// RetentionOptions times the background retention of versions, the changelog and the Admin API's
// done operations (ADR-024 §8); the zero value is the ADR's.
type RetentionOptions struct {
	// Every is how often a pass runs. Default 1 minute.
	Every time.Duration
	// Changelog is how long changelog rows are kept: the hub fills gaps and settles unknown
	// outcomes from them for up to about 10 seconds, so less than MinChangelogRetention is
	// raised to it. Less than the store's Options.ChangelogRetention, or zero, is raised to that.
	Changelog time.Duration
	// Operations is how long a done operation stays readable (GetOperation, ListOperations).
	// Default 30 days; production's is not recorded.
	Operations time.Duration
	// Batch is the most rows one delete takes. Default 1,000.
	Batch int
	// MaxBatches bounds the deletes of one table of one database in a pass, so that one busy
	// database neither holds the pass nor starves the others; the rest waits for the next pass.
	// Default 100.
	MaxBatches int
	Log        *slog.Logger
}

// MinChangelogRetention is the least changelog retention: well above the hub's 10 seconds.
const MinChangelogRetention = time.Minute

// changelogRetention is a configured changelog retention with its default and least.
func changelogRetention(d time.Duration) time.Duration {
	return max(cmp.Or(d, time.Hour), MinChangelogRetention)
}

// withDefaults fills o's zero values, and keeps the changelog at least the store's retention.
func (o RetentionOptions) withDefaults(s *PGStore) RetentionOptions {
	o.Every = cmp.Or(o.Every, time.Minute)
	o.Changelog = max(o.Changelog, s.changelogRetention)
	o.Operations = cmp.Or(o.Operations, 30*24*time.Hour)
	o.Batch = cmp.Or(o.Batch, 1000)
	o.MaxBatches = cmp.Or(o.MaxBatches, 100)
	if o.Log == nil {
		o.Log = slog.Default()
	}
	return o
}

// Pruned counts the rows a retention pass deleted.
type Pruned struct {
	Versions, Changelog, Operations int64
}

// RunRetention runs a retention pass every o.Every until ctx ends. A pass runs only while this
// process holds the single-instance lock (ADR-024 §7), and stops between batches when it loses it.
func (s *PGStore) RunRetention(ctx context.Context, o RetentionOptions) {
	o = o.withDefaults(s)
	t := time.NewTicker(o.Every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if s.serving() != nil {
			continue
		}
		n, err := s.Prune(ctx, o)
		if err != nil && ctx.Err() == nil {
			o.Log.Error("documents: retention", "error", err)
		}
		if n.Versions > 0 || n.Changelog > 0 || n.Operations > 0 {
			o.Log.Debug("documents: retention", "versions", n.Versions, "changelog", n.Changelog, "operations", n.Operations)
		}
	}
}

// Prune runs one retention pass over every database: versions that ended before the database's
// version_retention window, changelog rows older than o.Changelog, and operations done and older
// than o.Operations, in batches of o.Batch, at
// most o.MaxBatches of each per database. It returns ErrNotServing, with what it deleted, when
// the process does not hold the lock.
func (s *PGStore) Prune(ctx context.Context, o RetentionOptions) (Pruned, error) {
	o = o.withDefaults(s)
	var n Pruned
	if err := s.serving(); err != nil {
		return n, err
	}
	dbs, err := s.q.ListAllDatabases(ctx)
	if err != nil {
		return n, fmt.Errorf("documents: retention: list the databases: %w", err)
	}
	for _, db := range dbs {
		v, err := s.batches(ctx, o, func(ctx context.Context) (int64, error) {
			return s.q.PruneVersions(ctx, queries.PruneVersionsParams{
				ProjectNumber: db.ProjectNumber, DatabaseID: db.DatabaseID, Batch: int32(o.Batch)})
		})
		n.Versions += v
		if err != nil {
			return n, fmt.Errorf("documents: retention: versions of %d/%s: %w", db.ProjectNumber, db.DatabaseID, err)
		}
		c, err := s.batches(ctx, o, func(ctx context.Context) (int64, error) {
			return s.q.PruneChangelog(ctx, queries.PruneChangelogParams{
				ProjectNumber: db.ProjectNumber, DatabaseID: db.DatabaseID, Batch: int32(o.Batch),
				RetentionSecs: o.Changelog.Seconds()})
		})
		n.Changelog += c
		if err != nil {
			return n, fmt.Errorf("documents: retention: changelog of %d/%s: %w", db.ProjectNumber, db.DatabaseID, err)
		}
		ops, err := s.batches(ctx, o, func(ctx context.Context) (int64, error) {
			return s.q.PruneOperations(ctx, queries.PruneOperationsParams{
				ProjectNumber: db.ProjectNumber, DatabaseID: db.DatabaseID, Batch: int32(o.Batch),
				RetentionSecs: o.Operations.Seconds()})
		})
		n.Operations += ops
		if err != nil {
			return n, fmt.Errorf("documents: retention: operations of %d/%s: %w", db.ProjectNumber, db.DatabaseID, err)
		}
	}
	return n, nil
}

// batches runs del until it deletes less than a batch, or o.MaxBatches times, and returns the
// rows deleted. Each delete is its own statement and transaction, under a timeout.
func (s *PGStore) batches(ctx context.Context, o RetentionOptions, del func(context.Context) (int64, error)) (int64, error) {
	var total int64
	for range o.MaxBatches {
		if err := s.serving(); err != nil {
			return total, err
		}
		n, err := deleteBatch(ctx, del)
		total += n
		if err != nil || n < int64(o.Batch) {
			return total, err
		}
	}
	return total, nil
}

// batchTimeout bounds one batched delete.
const batchTimeout = 30 * time.Second

func deleteBatch(ctx context.Context, del func(context.Context) (int64, error)) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, batchTimeout)
	defer cancel()
	return del(ctx)
}
