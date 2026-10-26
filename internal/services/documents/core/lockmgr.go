package core

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// The lock manager (ADR-024 §6): shared, intent and exclusive locks on documents and on the
// scopes queries read, held in this process for as long as a transaction is open, so that no
// Postgres connection is held between two requests. One table per database, under one mutex.
//
// Wound-wait, as production showed (f0-contention.json; .cache/briefs/research/f0-contention.md):
//
//   - a transaction's age is taken at its first lock request, not at Begin (s3b); a retry keeps
//     the age of the attempt it retries (ADR-024 §6.2);
//   - an older requester wounds every younger transaction in its way that is not committing: one
//     holding a conflicting lock, or queued ahead of it for one. The wounded one's locks go at
//     once; its wait, or its next call, fails ABORTED with the wound text (s1, s3a);
//   - a younger requester waits for the older one (s2), up to the wait timeout, about 20 s in
//     production (s6), after which it is ABORTED with "Too much contention" and its locks go;
//   - idleness ends nothing and wounds nothing: an idle holder keeps its locks until it ends or
//     its lifetime does (s5: a commit after 70 s idle succeeds; f2-transactions.json lifetime
//     idle_70s_then_plain_write: a writer waits 20 s for an idle holder, then the holder commits).
//
// Every edge of the wait-for graph so then runs from a younger transaction to an older one, or to
// one that is committing and so waits for nothing: there is no cycle, and the wait timeout is a
// backstop for a holder that sits between two requests.
//
// Queues are strictly FIFO: a request compatible with the holders still waits behind one queued
// before it. An upgrade (a transaction asking more of a lock it holds) goes ahead of the requests
// that hold nothing, which would wait for it anyway.

// lockMode is a lock's strength on one name (ADR-024 §6.1). Documents take shared (a read) and
// exclusive (a write); scopes take shared (a query or listing of them) and intent (a write of a
// document in them).
type lockMode uint8

const (
	lockShared lockMode = iota + 1
	lockIntent
	lockExclusive
)

// join is the mode that grants both a and b. Shared with intent conflicts with everything either
// conflicts with, which here is every mode: exclusive.
func join(a, b lockMode) lockMode {
	switch {
	case a == 0 || a == b:
		return b
	case b == 0:
		return a
	}
	return lockExclusive
}

// compatible reports whether two transactions may hold a and b on one name at once: shared with
// shared, intent with intent.
func compatible(a, b lockMode) bool { return a == b && a != lockExclusive }

// The lock names, unique within a database: a document by its stored name key, a collection by
// its prefix key's hash, a collection group by its id's hash, and the database.
func docLock(k docKeys) string        { return "d" + string(k.stored) }
func collectionLock(k docKeys) string { return "c" + string(k.parentHash) }
func groupLock(k docKeys) string      { return "g" + string(k.cgHash) }

const databaseLock = "*"

// lockTxn is a transaction as the lock manager sees it: a pessimistic transaction for its whole
// life, or a commit (without a transaction, or of an optimistic one) for the time of its locks.
type lockTxn struct {
	age        uint64 // wound-wait order, smaller is older; 0 until the first lock request
	held       map[string]lockMode
	waiting    *lockRequest
	committing bool // holds every lock its commit needs: never wounded, never waits again
	done       bool
	err        error     // why it ended, for its waiter and its next call
	bounded    bool      // holds at most MaxLocks locks
	waitUntil  time.Time // when set, the end of every wait, instead of WaitTimeout from its start
}

type lockRequest struct {
	txn     *lockTxn
	name    string
	mode    lockMode // what the transaction will hold once granted
	upgrade bool
	granted chan struct{}
	err     error // set when the transaction ended while waiting
}

type lockState struct {
	holders map[*lockTxn]lockMode
	queue   []*lockRequest
}

// lockManager holds every database's lock table.
type lockManager struct {
	cfg   *TxnConfig
	stats *txnCounters
	ages  atomic.Uint64

	mu     sync.Mutex
	tables map[dbKey]*lockTable
}

func newLockManager(cfg *TxnConfig, stats *txnCounters) *lockManager {
	return &lockManager{cfg: cfg, stats: stats, tables: map[dbKey]*lockTable{}}
}

// table returns a database's lock table.
func (m *lockManager) table(db DB) *lockTable {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := dbKey{db.Project, db.ID}
	t := m.tables[k]
	if t == nil {
		t = &lockTable{m: m, locks: map[string]*lockState{}, txns: map[*lockTxn]struct{}{}}
		m.tables[k] = t
	}
	return t
}

// lockTable is one database's locks.
type lockTable struct {
	m     *lockManager
	mu    sync.Mutex
	locks map[string]*lockState
	txns  map[*lockTxn]struct{} // every transaction not yet ended
}

// begin returns a new transaction of the table with age (0: taken at its first lock request).
// A commit's own transaction (forCommit) starts inside its call, and its locks are not bounded:
// a commit's writes are not (ADR-025 §7.5).
func (t *lockTable) begin(age uint64, forCommit bool) *lockTxn {
	t.mu.Lock()
	defer t.mu.Unlock()
	lt := &lockTxn{age: age, held: map[string]lockMode{}, bounded: !forCommit}
	t.txns[lt] = struct{}{}
	return lt
}

// state reports why the transaction ended; nil while it is open.
func (t *lockTable) state(lt *lockTxn) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if lt.done {
		return lt.err
	}
	return nil
}

// age returns the transaction's age, 0 when it never asked for a lock.
func (t *lockTable) age(lt *lockTxn) uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return lt.age
}

// lockSpec is one lock a call needs.
type lockSpec struct {
	name string
	mode lockMode
}

// acquireAll takes every lock in order, stopping at the first that fails.
func (t *lockTable) acquireAll(ctx context.Context, lt *lockTxn, specs []lockSpec) error {
	for _, s := range specs {
		if err := t.acquire(ctx, lt, s.name, s.mode); err != nil {
			return err
		}
	}
	return nil
}

// acquire takes name in mode for lt, waiting in line. It fails with the reason lt ended: wounded
// (ErrWounded), timed out (ErrContention: lt ends too) or over a limit. A cancelled ctx withdraws
// the request and leaves lt open.
func (t *lockTable) acquire(ctx context.Context, lt *lockTxn, name string, mode lockMode) error {
	cfg := t.m.cfg
	t.mu.Lock()
	if lt.done {
		t.mu.Unlock()
		return lt.err
	}
	if lt.committing {
		// Its locks are all taken; the commit path never asks for more.
		t.mu.Unlock()
		return ErrTransactionExpired
	}
	have := lt.held[name]
	want := join(have, mode)
	if want == have {
		t.mu.Unlock()
		return nil
	}
	if have == 0 && lt.bounded && len(lt.held) >= cfg.MaxLocks {
		t.endLocked(lt, errTooManyLocks)
		t.mu.Unlock()
		return errTooManyLocks
	}
	if lt.age == 0 {
		lt.age = t.m.ages.Add(1)
	}
	ls := t.locks[name]
	if ls == nil {
		ls = &lockState{holders: map[*lockTxn]lockMode{}}
		t.locks[name] = ls
	}
	req := &lockRequest{txn: lt, name: name, mode: want, upgrade: have != 0, granted: make(chan struct{})}
	if (req.upgrade || len(ls.queue) == 0) && grantable(ls, req) {
		grant(ls, req)
		t.mu.Unlock()
		return nil
	}
	if req.upgrade {
		i := slices.IndexFunc(ls.queue, func(r *lockRequest) bool { return !r.upgrade })
		if i < 0 {
			i = len(ls.queue)
		}
		ls.queue = slices.Insert(ls.queue, i, req)
	} else {
		ls.queue = append(ls.queue, req)
	}
	lt.waiting = req
	t.m.stats.waits.Add(1)
	t.woundLocked(ls)
	t.mu.Unlock()

	wait := cfg.WaitTimeout
	if !lt.waitUntil.IsZero() {
		wait = time.Until(lt.waitUntil)
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	timedOut := false
	select {
	case <-req.granted:
	case <-timer.C:
		timedOut = true
	case <-ctx.Done():
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	select {
	case <-req.granted:
		return req.err
	default:
	}
	if timedOut {
		t.m.stats.timeouts.Add(1)
		t.endLocked(lt, ErrContention)
		return ErrContention
	}
	t.withdrawLocked(req)
	return ctx.Err()
}

// grantable reports whether r is compatible with every other holder of its lock.
func grantable(ls *lockState, r *lockRequest) bool {
	for h, mode := range ls.holders {
		if h != r.txn && !compatible(mode, r.mode) {
			return false
		}
	}
	return true
}

func grant(ls *lockState, r *lockRequest) {
	ls.holders[r.txn] = r.mode
	r.txn.held[r.name] = r.mode
}

// woundLocked ends every transaction a waiter of ls may wound, until none is left.
func (t *lockTable) woundLocked(ls *lockState) {
	for {
		v := t.victimLocked(ls)
		if v == nil {
			return
		}
		t.m.stats.wounds.Add(1)
		t.endLocked(v, ErrWounded)
	}
}

// victimLocked returns a transaction that a waiter of ls waits for and may wound: a conflicting
// holder, or a conflicting request queued ahead of it, that is not committing and is younger
// than the waiter.
func (t *lockTable) victimLocked(ls *lockState) *lockTxn {
	for i, r := range ls.queue {
		w := r.txn
		for h, mode := range ls.holders {
			if h != w && !compatible(mode, r.mode) && woundable(h, w) {
				return h
			}
		}
		for _, a := range ls.queue[:i] {
			if a.txn != w && !compatible(a.mode, r.mode) && woundable(a.txn, w) {
				return a.txn
			}
		}
	}
	return nil
}

// woundable reports whether by may wound v: v is younger and not committing. Idleness does not
// make a transaction woundable: production's idle holder of a read lock keeps it, the writer
// waits out its 20 s and gets the contention text, and the holder then commits (f2-transactions.json
// lifetime idle_70s_then_plain_write).
func woundable(v, by *lockTxn) bool {
	return !v.committing && !v.done && v.age > by.age
}

// markCommitting records that lt holds every lock its commit needs: from here it is never
// wounded and never expires, and its locks go with release. It fails if lt ended before.
func (t *lockTable) markCommitting(lt *lockTxn) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if lt.done {
		return lt.err
	}
	lt.committing = true
	return nil
}

// release ends lt (a commit or a rollback) and frees its locks; it may run more than once.
func (t *lockTable) release(lt *lockTxn) { t.end(lt, ErrTransactionExpired) }

// end ends lt with err, unless it ended before, and frees its locks.
func (t *lockTable) end(lt *lockTxn, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.endLocked(lt, err)
}

func (t *lockTable) endLocked(lt *lockTxn, err error) {
	if lt.done {
		return
	}
	lt.done, lt.err = true, err
	delete(t.txns, lt)
	if r := lt.waiting; r != nil {
		r.err = err
		t.withdrawLocked(r)
		close(r.granted)
	}
	for name := range lt.held {
		ls := t.locks[name]
		delete(ls.holders, lt)
		t.wakeLocked(name, ls)
	}
	clear(lt.held)
}

// withdrawLocked takes a waiting request out of its queue.
func (t *lockTable) withdrawLocked(r *lockRequest) {
	r.txn.waiting = nil
	ls := t.locks[r.name]
	if i := slices.Index(ls.queue, r); i >= 0 {
		ls.queue = slices.Delete(ls.queue, i, i+1)
	}
	t.wakeLocked(r.name, ls)
}

// wakeLocked grants the head of the queue for as long as it is compatible.
func (t *lockTable) wakeLocked(name string, ls *lockState) {
	for len(ls.queue) > 0 && grantable(ls, ls.queue[0]) {
		r := ls.queue[0]
		ls.queue = ls.queue[1:]
		r.txn.waiting = nil
		grant(ls, r)
		close(r.granted)
	}
	if len(ls.holders) == 0 && len(ls.queue) == 0 {
		delete(t.locks, name)
	}
}

// open reports how many transactions of the table are not yet ended, and how many locks are held
// or asked for: tests check that everything is freed.
func (t *lockTable) open() (txns, locks int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.txns), len(t.locks)
}
