package core

import "context"

// Boot runs before the listener opens (ADR-022 §5.1): it takes the data directory's lock, binds
// the directory to this database through the INSTANCE marker and storage.instance, lists the
// files under p/ and s/ as deletion candidates, reconciles every project's quota, and warns when
// Postgres runs with fsync off.
func (s *Service) Boot(ctx context.Context) error {
	panic("S2-D: Boot")
}
