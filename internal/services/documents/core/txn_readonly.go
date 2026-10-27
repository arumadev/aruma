package core

import (
	"context"
	"time"
)

// Read-only transactions (ADR-025 §7.4): no locks; every read is at one snapshot, each read a
// statement of its own at that time. The snapshot is the read_time given at Begin, or else the
// read_time of the transaction's first read, which reads at the latest commit (ADR-024 §3):
// production's query in a read-only transaction sees a commit made after its Begin and before
// its first read (f2-transactions.json read.run_query_read_only). A commit with writes is
// refused; one without ends the transaction.

// snapshot checks a read-only transaction's read_time t against the database's window at
// Begin (too old: FAILED_PRECONDITION; future: INVALID_ARGUMENT, f1-get.json). With no t it only
// checks that the database exists ('(default)' is created), as production answers NOT_FOUND at
// Begin; the snapshot is then fixed by the first read (atSnapshot).
func (s *PGStore) snapshot(ctx context.Context, db DB, t time.Time) (time.Time, error) {
	if t.IsZero() {
		_, err := s.Database(ctx, db)
		return time.Time{}, err
	}
	h, _, err := s.read(ctx, db, t, func(t time.Time) (string, []any) { return s.headStatement(ctx, db, t) })
	if err != nil {
		return time.Time{}, err
	}
	return h.readTime, nil
}

// atSnapshot runs read, a read in the read-only transaction tx, at its snapshot. When the
// snapshot is not fixed yet the read runs at the latest commit (t zero) and its read time
// becomes the snapshot; tx.snap is held meanwhile, so reads that come during it wait and read
// at that time, and one time is fixed. A failed first read fixes none.
func atSnapshot[R any](tx *txn, read func(t time.Time) (R, time.Time, error)) (R, time.Time, error) {
	tx.snap.Lock()
	if t := tx.readTime; !t.IsZero() {
		tx.snap.Unlock()
		return read(t)
	}
	defer tx.snap.Unlock()
	res, t, err := read(time.Time{})
	if err == nil {
		tx.readTime = t
	}
	return res, t, err
}
