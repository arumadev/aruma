package core

import (
	"context"
	"slices"
)

// Optimistic transactions (ADR-025 §7.2, ADR-024 §6.4): an end user's read-write transaction, or
// an admin's that asks for OPTIMISTIC. It takes no lock while open: each read is recorded with
// the version it saw, each listing with its request and result. At commit it takes, as a
// transaction of its own, shared locks on what it read and on the scopes it listed and exclusive
// locks on what it writes, so that no commit changes any of it until this one's ends; then it
// reads again what it read and does not write, lists again, and checks the documents it writes
// under their row locks. Any difference is ABORTED, which the clients retry.

// listRead is a listing an optimistic transaction made, and what it returned.
type listRead struct {
	req  ListRequest // with no transaction
	lock string
	docs []readVersion
}

func versions(docs []Document) []readVersion {
	out := make([]readVersion, len(docs))
	for i, d := range docs {
		out[i] = readVersion{path: d.Path, exists: d.Exists(), updateTime: d.UpdateTime}
	}
	return out
}

// lockOptimistic takes an optimistic transaction's commit locks and checks its reads under them.
func (s *PGStore) lockOptimistic(ctx context.Context, db DB, p *plan, c *commitTxn) (func(), error) {
	written := make(map[string]bool, len(p.docs))
	for _, d := range p.docs {
		written[string(d.keys.stored)] = true
	}
	var more []lockSpec
	var unwritten []readVersion
	for k, v := range c.reads {
		if !written[k] {
			more = append(more, lockSpec{"d" + k, lockShared})
			unwritten = append(unwritten, v)
		}
	}
	for _, l := range c.lists {
		more = append(more, lockSpec{l.lock, lockShared})
	}
	conflict := func() error {
		s.txns.stats.conflicts.Add(1)
		return ErrReadsChanged
	}
	p.verify = verifyWritten(p, c.reads, conflict)
	return s.lockWrites(ctx, db, p, c.tx.age, more, func() error {
		if len(unwritten) > 0 {
			paths := make([]Path, len(unwritten))
			for i, v := range unwritten {
				paths[i] = v.path
			}
			docs, _, err := s.BatchGet(ctx, db, paths, ReadOptions{})
			if err != nil {
				return err
			}
			for i, d := range docs {
				if !unwritten[i].same(d.Exists(), d.UpdateTime) {
					return conflict()
				}
			}
		}
		for _, l := range c.lists {
			res, err := s.ListDocuments(ctx, db, l.req)
			if err != nil {
				return err
			}
			if !slices.EqualFunc(l.docs, res.Documents, func(v readVersion, d Document) bool {
				return v.path.String() == d.Path.String() && v.same(d.Exists(), d.UpdateTime)
			}) {
				return conflict()
			}
		}
		return nil
	})
}
