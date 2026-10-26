package core

import (
	"context"
	"time"
)

// Expiry (ADR-024 §6.3, ADR-025 §7.5). A transaction ends MaxLifetime (270 s) after Begin; a call
// that finds it past that ends it at once, and the sweep ends the ones no call comes for, which
// may hold locks. Idleness alone ends nothing: production commits after 70 s idle (f0-contention
// s5). A transaction idle past IdleTimeout (60 s) may be wounded by any waiter instead; a waiter
// meets an idle holder when it queues, and the sweep wounds a holder that became idle while a
// waiter waited. An ended transaction's record stays MaxLifetime longer, so that
// retry_transaction finds its age and its id answers the expired text.

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

// SweepTransactions ends the transactions past their lifetime, wounds the idle holders a waiter
// waits for, and forgets the records of transactions ended long enough ago.
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
		case tx.ended && now.Sub(tx.endedAt) > r.cfg.MaxLifetime:
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
	r.locks.each(func(t *lockTable) { t.sweep() })
}
