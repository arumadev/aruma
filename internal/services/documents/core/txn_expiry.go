package core

import (
	"context"
	"time"
)

// Expiry (ADR-024 §6.3, ADR-025 §7.5). A transaction ends MaxLifetime (270 s) after Begin; a call
// that finds it past that ends it at once, and the sweep ends the ones no call comes for, which
// may hold locks. Idleness alone ends nothing and wounds nothing: production commits after 70 s
// idle, and keeps an idle holder's locks while a writer waits (f0-contention s5,
// f2-transactions.json lifetime). An ended transaction's record stays until forgetAfter (300 s)
// from its Begin, so that retry_transaction finds its age and its id answers the expired text;
// then its id is "Invalid transaction.", as production's is.

// sweepEvery is how often RunTransactions sweeps.
const sweepEvery = time.Second

// RunTransactions sweeps the transactions every second until ctx ends. The service runs it while
// the store serves.
func (s *PGStore) RunTransactions(ctx context.Context) {
	tick := time.NewTicker(sweepEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.SweepTransactions()
		}
	}
}

// SweepTransactions ends the transactions past their lifetime and forgets the records of those
// past forgetAfter.
func (s *PGStore) SweepTransactions() {
	r := s.txns
	now := r.cfg.now()
	r.mu.Lock()
	all := make([]*txn, 0, len(r.all))
	for _, tx := range r.all {
		all = append(all, tx)
	}
	r.mu.Unlock()
	for _, tx := range all {
		tx.mu.Lock()
		switch {
		case tx.ended && now.Sub(tx.issued) > r.cfg.forgetAfter():
			r.mu.Lock()
			if r.all[tx.num] == tx {
				delete(r.all, tx.num)
			}
			r.mu.Unlock()
		case !tx.ended && !tx.committed && now.Sub(tx.issued) > r.cfg.MaxLifetime:
			r.stats.expired.Add(1)
			r.finishLocked(tx)
		}
		tx.mu.Unlock()
	}
}
