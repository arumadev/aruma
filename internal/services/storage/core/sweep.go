package core

import (
	"context"
	"fmt"
	"time"

	"github.com/arumadev/aruma/internal/services/storage/queries"
)

// The sweeps (ADR-022 §5.1, §5.2). One process writes the data directory (Boot holds its lock),
// so a file that existed before this process wrote anything, and that no object, no session and
// no outbox row points to, is an orphan for certain: no writer is in the middle of it. Boot lists
// those files before the listener opens, and Run deletes the ones the rows do not name. Nothing
// else is ever deleted by a sweep: the periodic one only reports, because what it finds is not
// certain (a write in flight has a file and no row yet).

// sweepBatch is how many keys one statement checks.
const sweepBatch = 1000

// reportAge is how old a file without a row must be before the periodic sweep reports it: a
// write in flight has its file before its row.
const reportAge = time.Hour

// deleteCandidates deletes Boot's candidates that no row points to, one statement per batch. A
// file written since Boot is never a candidate, and an orphan's key is in no row and in no
// request, so nothing can point to it between the check and the delete.
func (s *Service) deleteCandidates(ctx context.Context) error {
	for len(s.candidates) > 0 {
		batch := s.candidates[:min(sweepBatch, len(s.candidates))]
		orphans, err := s.reads.UnreferencedKeys(ctx, batch)
		if err != nil {
			return fmt.Errorf("%w: check the sweep's candidates: %w", ErrUnavailable, err)
		}
		for _, k := range orphans {
			if err := s.store.Delete(k); err != nil {
				s.log.Warn("storage: sweep: delete an orphan; the next boot tries again", "key", k, "error", err)
				continue
			}
			s.metrics.SweepDeleted.Inc()
		}
		s.candidates = s.candidates[len(batch):]
	}
	s.candidates = nil
	return nil
}

// reportInconsistencies is the periodic sweep: it counts and logs files without a row (older than
// reportAge) and objects whose file is missing, and deletes nothing. The object keys are read
// before the walk, so an object committed during it is not taken for one without a file, and
// one deleted during it keeps its file for gc_grace.
func (s *Service) reportInconsistencies(ctx context.Context) error {
	var objects []string
	for after := ""; ; {
		page, err := s.reads.ListObjectKeys(ctx, queries.ListObjectKeysParams{After: after, N: sweepBatch})
		if err != nil {
			return fmt.Errorf("%w: list the object keys: %w", ErrUnavailable, err)
		}
		objects = append(objects, page...)
		if len(page) < sweepBatch {
			break
		}
		after = page[len(page)-1]
	}
	files := map[string]bool{}
	var old []string
	cutoff := s.now().Add(-reportAge)
	for _, prefix := range []string{"p/", "s/"} {
		err := s.store.Walk(prefix, func(key string, mod time.Time) error {
			files[key] = true
			if mod.Before(cutoff) {
				old = append(old, key)
			}
			return ctx.Err()
		})
		if err != nil {
			return fmt.Errorf("%w: walk %s: %w", ErrUnavailable, prefix, err)
		}
	}
	for len(old) > 0 {
		batch := old[:min(sweepBatch, len(old))]
		orphans, err := s.reads.UnreferencedKeys(ctx, batch)
		if err != nil {
			return fmt.Errorf("%w: check the files: %w", ErrUnavailable, err)
		}
		for _, k := range orphans {
			s.metrics.SweepReported.WithLabelValues("file_without_row").Inc()
			s.log.Warn("storage: sweep: a file no row points to", "key", k)
		}
		old = old[len(batch):]
	}
	for _, k := range objects {
		if !files[k] {
			s.metrics.SweepReported.WithLabelValues("row_without_file").Inc()
			s.log.Error("storage: sweep: an object's file is missing", "key", k)
		}
	}
	return nil
}
