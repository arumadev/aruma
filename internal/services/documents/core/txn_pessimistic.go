package core

import (
	"context"
	"crypto/sha256"
)

// Pessimistic transactions (ADR-025 §7.3, ADR-024 §6): every read takes a shared lock before it
// reads, on each document (existing or not) or on the scope a listing reads, and keeps it until
// the transaction ends. The commit takes the exclusive locks of its writes, then is marked
// committing, which no wound reaches; its first batch checks that each document it writes that
// the transaction read is still as read, which only a lock manager bug or a second process can
// break (ADR-024 §6.4).

// readLocks returns the shared locks of a read of paths.
func readLocks(paths []Path) []lockSpec {
	specs := make([]lockSpec, len(paths))
	for i, p := range paths {
		specs[i] = lockSpec{"d" + string(storedKey(nameKey(p))), lockShared}
	}
	return specs
}

// listLock returns the scope a listing reads (ADR-024 §6.1): its collection; or the database for
// every collection under a parent, and for show_missing, whose missing documents come and go with
// writes to other collections, below them.
func listLock(r ListRequest) string {
	if r.CollectionID == "" || r.ShowMissing {
		return databaseLock
	}
	h := sha256.Sum256(collectionPrefix(r.Parent, r.CollectionID))
	return "c" + string(h[:])
}

// lockPessimistic takes a pessimistic transaction's write locks for its commit.
func (s *PGStore) lockPessimistic(ctx context.Context, db DB, p *plan, c *commitTxn) (func(), error) {
	table, lt := c.tx.table, c.tx.lt
	if err := table.state(lt); err != nil {
		return nil, err
	}
	if err := table.acquireAll(ctx, lt, writeLocks(p)); err != nil {
		return nil, err
	}
	if err := s.authorize(ctx, db, p); err != nil {
		return nil, err
	}
	if err := table.markCommitting(lt); err != nil {
		return nil, err
	}
	p.verify = verifyWritten(p, c.reads, func() error {
		s.txns.stats.violations.Add(1)
		return ErrReadsChanged
	})
	return func() { table.release(lt) }, nil
}
