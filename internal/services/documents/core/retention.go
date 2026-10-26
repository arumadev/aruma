package core

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/arumadev/aruma/internal/services/documents/queries"
)

// RetentionOptions times the background retention of versions and the changelog (ADR-024 §8);
// the zero value is the ADR's.
type RetentionOptions struct {
	// Every is how often a pass runs. Default 1 minute.
	Every time.Duration
	// Changelog is how long changelog rows are kept: the hub fills gaps and settles unknown
	// outcomes from them for up to about 10 seconds, so less than MinChangelogRetention is
	// raised to it. Default 1 hour.
	Changelog time.Duration
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

func (o RetentionOptions) withDefaults() RetentionOptions {
	o.Every = cmp.Or(o.Every, time.Minute)
	o.Changelog = max(cmp.Or(o.Changelog, time.Hour), MinChangelogRetention)
	o.Batch = cmp.Or(o.Batch, 1000)
	o.MaxBatches = cmp.Or(o.MaxBatches, 100)
	if o.Log == nil {
		o.Log = slog.Default()
	}
	return o
}

// Pruned counts the rows a retention pass deleted.
type Pruned struct {
	Versions, Changelog int64
}

// RunRetention runs a retention pass every o.Every until ctx ends. A pass runs only while this
// process holds the single-instance lock (ADR-024 §7), and stops between batches when it loses it.
func (s *PGStore) RunRetention(ctx context.Context, o RetentionOptions) {
	o = o.withDefaults()
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
		if n.Versions > 0 || n.Changelog > 0 {
			o.Log.Debug("documents: retention", "versions", n.Versions, "changelog", n.Changelog)
		}
	}
}

// Prune runs one retention pass over every database: versions that ended before the database's
// version_retention window, and changelog rows older than o.Changelog, in batches of o.Batch, at
// most o.MaxBatches of each per database. It returns ErrNotServing, with what it deleted, when
// the process does not hold the lock.
func (s *PGStore) Prune(ctx context.Context, o RetentionOptions) (Pruned, error) {
	o = o.withDefaults()
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
