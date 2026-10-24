package core

import "context"

// Run works after the listener opened, until ctx ends: it deletes Boot's candidates that no row
// points to, runs the blob_gc worker, expires sessions, and reports what the periodic sweep finds.
func (s *Service) Run(ctx context.Context) error {
	panic("S2-D: Run")
}
