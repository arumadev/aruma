package core

import (
	"context"
	"fmt"

	"github.com/arumadev/aruma/internal/services/storage/blob"
	"github.com/arumadev/aruma/internal/services/storage/queries"
)

// PurgeProject removes a purged project's files and rows: its staging files, then its object
// files, then its rows. It is idempotent and resumes where it stopped; tenancy calls it through
// OnProjectPurged until it succeeds (ADR-022 §3).
//
// The files go before the rows, so a purge cut in the middle leaves rows to find the project by
// on the next run, and never a file no row names. The prefixes are the project's alone: a
// project number is never used again. A purged project's bucket resolves to nothing, so no
// request writes in it meanwhile.
func (s *Service) PurgeProject(ctx context.Context, project int64) error {
	leave, err := s.enter()
	if err != nil {
		return err
	}
	defer leave()
	if err := s.store.DeletePrefix(blob.StagingPrefix(project)); err != nil {
		return fmt.Errorf("storage: purge the staging files of project %d: %w", project, err)
	}
	hook("purge.staging")
	if err := s.store.DeletePrefix(blob.ProjectPrefix(project)); err != nil {
		return fmt.Errorf("storage: purge the files of project %d: %w", project, err)
	}
	hook("purge.files")
	err = s.inTx(ctx, func(q *queries.Queries) error {
		if err := q.PurgeUploadSessions(ctx, project); err != nil {
			return err
		}
		if err := q.PurgeObjects(ctx, project); err != nil {
			return err
		}
		if err := q.PurgeBlobGC(ctx, project); err != nil {
			return err
		}
		if err := q.PurgeUsage(ctx, project); err != nil {
			return err
		}
		return q.PurgeBuckets(ctx, project)
	})
	if err != nil {
		return fmt.Errorf("storage: purge the rows of project %d: %w", project, err)
	}
	return nil
}
