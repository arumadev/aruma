package core

import (
	"context"
	"time"
)

// Read-only transactions (ADR-025 §7.4): no locks; every read is at one snapshot, the read_time
// given at Begin or else the last commit's commit_time then (ADR-024 §3), each read a statement
// of its own at that time. A commit with writes is refused; one without ends the transaction.

// snapshot returns a read-only transaction's read time: t once checked against the database's
// window (too old: FAILED_PRECONDITION; future: INVALID_ARGUMENT, f1-get.json), or the last
// commit's commit_time.
func (s *PGStore) snapshot(ctx context.Context, db DB, t time.Time) (time.Time, error) {
	h, _, err := s.read(ctx, db, t, func(t time.Time) (string, []any) { return s.headStatement(ctx, db, t) })
	if err != nil {
		return time.Time{}, err
	}
	return h.readTime, nil
}
