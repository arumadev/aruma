package core

import "context"

// PurgeProject removes a purged project's files and rows: its staging files, then its object
// files, then its rows. It is idempotent and resumes where it stopped; tenancy calls it through
// OnProjectPurged until it succeeds (ADR-022 §3).
func (s *Service) PurgeProject(ctx context.Context, project int64) error {
	panic("S2-D: PurgeProject")
}
