package core

import (
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"maps"
	"sync"
	"sync/atomic"
	"time"
)

// Transactions (ADR-025 §7, ADR-024 §6). Three models, chosen by the principal and the options:
//
//   - read-only (txn_readonly.go): one snapshot, its read_time or the last commit at Begin; no
//     locks;
//   - pessimistic (txn_pessimistic.go), an admin's read-write transaction by default: a shared
//     lock on each document read and on each scope listed, held in the lock manager until the
//     transaction ends; exclusive locks at commit;
//   - optimistic (txn_optimistic.go), an end user's read-write transaction, which must ask for
//     OPTIMISTIC, or an admin's that asks: no locks while open; the versions read are recorded
//     and checked at commit under short-lived locks.
//
// A transaction lives in this process; no Postgres connection is held between two requests. Its
// id is authenticated (txnID): a forged or foreign id is INVALID_ARGUMENT "Invalid transaction.";
// an authentic id of a transaction that is over, or that this process does not know (issued
// before a restart), is ABORTED "The referenced transaction has expired or is no longer valid.".
// A transaction ends at its commit, whatever the commit's outcome, at Rollback, when it is
// wounded or times out waiting for a lock (the call that meets it answers why, later calls the
// expired text), and 270 s after Begin (txn_expiry.go).

// TransactionOptions are BeginTransaction's options (google.firestore.v1.TransactionOptions).
type TransactionOptions struct {
	// ReadOnly begins a read-only transaction, at ReadTime when set, else at the last commit.
	ReadOnly bool
	ReadTime time.Time
	// Concurrency is the read-write transaction's concurrency_mode.
	Concurrency Concurrency
	// Retry is read_write.retry_transaction: the id of the attempt this one retries. The new
	// transaction keeps its age in wound-wait, so a retried transaction only grows older and is
	// not starved (ADR-024 §6.2); the attempt, if still open, is rolled back.
	Retry []byte
}

// Concurrency is a read-write transaction's concurrency_mode.
type Concurrency uint8

const (
	// ConcurrencyDefault is the database's mode: PESSIMISTIC (ADR-025 §7.3).
	ConcurrencyDefault Concurrency = iota
	ConcurrencyOptimistic
	ConcurrencyPessimistic
)

// TransactionRef names a transaction in a call: its id, and the principal that uses it, to which
// the id must have been issued.
type TransactionRef struct {
	ID []byte
	By Principal
}

// TxnConfig bounds the transactions (documents.limits.*, ADR-024 §6.3); the zero value is
// production's numbers where they are known.
type TxnConfig struct {
	// Key authenticates transaction ids: 32 bytes, derived from the master key (HKDF info
	// "aruma/v1/documents-transaction"), so that an id stays verifiable after a restart. Empty,
	// a random key per process: an id from before a restart is then forged, not expired.
	Key []byte
	// WaitTimeout bounds a lock wait (txn_wait_timeout): 20 s, production's (f0-contention s6).
	WaitTimeout time.Duration
	// IdleTimeout is how long a transaction may go without a call before any waiter may wound
	// it (txn_idle_timeout): 60 s. It does not end the transaction (f0-contention s5).
	IdleTimeout time.Duration
	// MaxLifetime ends a transaction this long after Begin (txn_max_lifetime): 270 s. An ended
	// transaction's record stays as long again, for retry_transaction and the expired text.
	MaxLifetime time.Duration
	// MaxOpen bounds a database's open pessimistic transactions (max_open_txns): 1,000.
	MaxOpen int
	// MaxOpenUnlocked bounds a database's open read-only and optimistic transactions, which
	// end users open: 10,000. Our own bound; ADR-024 §6.3 bounds the pessimistic ones only.
	MaxOpenUnlocked int
	// MaxLocks bounds a transaction's locks, and an optimistic one's recorded reads
	// (max_locks_per_txn): 10,000.
	MaxLocks int
	// Now is the clock; nil is time.Now. Tests move it to make transactions idle or old.
	Now func() time.Time
}

func (c *TxnConfig) defaults() {
	c.WaitTimeout = cmp.Or(c.WaitTimeout, 20*time.Second)
	c.IdleTimeout = cmp.Or(c.IdleTimeout, 60*time.Second)
	c.MaxLifetime = cmp.Or(c.MaxLifetime, 270*time.Second)
	c.MaxOpen = cmp.Or(c.MaxOpen, 1000)
	c.MaxOpenUnlocked = cmp.Or(c.MaxOpenUnlocked, 10000)
	c.MaxLocks = cmp.Or(c.MaxLocks, 10000)
	if c.Now == nil {
		c.Now = time.Now
	}
}

func (c *TxnConfig) now() time.Time { return c.Now() }

var (
	// ErrInvalidTransaction is an id this server did not issue, or issued for another database
	// or principal (f0-errors.json, txn_unknown.*; ADR-025 §7.5).
	ErrInvalidTransaction = &Error{InvalidArgument, "Invalid transaction."}
	// ErrTransactionExpired is an authentic id of a transaction that is over: committed, rolled
	// back, aborted, expired, or unknown to this process (f0-errors.json txn_reused; f0-contention
	// s5; ADR-025 §7.5).
	ErrTransactionExpired = &Error{Aborted, "The referenced transaction has expired or is no longer valid."}
	// ErrWounded is a transaction an older one wounded (f0-contention s1, s3a).
	ErrWounded = &Error{Aborted, "Aborted due to cross-transaction contention. This occurs when multiple transactions " +
		"attempt to access the same data, requiring Firestore to abort at least one in order to enforce serializability."}
	// ErrReadsChanged is a transaction's commit that found changed what the transaction read:
	// an optimistic one's, or a pessimistic one's after a broken lock. ErrWounded's text,
	// provisionally: production's for an optimistic conflict is unrecorded.
	ErrReadsChanged = &Error{Aborted, ErrWounded.Message}
	// ErrReadOnlyWrite is a commit with writes in a read-only transaction. Provisional: production's
	// answer is unrecorded.
	ErrReadOnlyWrite = &Error{InvalidArgument, "Cannot modify entities in a read-only transaction."}
	// ErrOptimisticRequired is an end user's read-write transaction that did not ask for
	// OPTIMISTIC (ADR-025 §7.2). Provisional: production's answer is unrecorded.
	ErrOptimisticRequired = &Error{InvalidArgument, "Third-party requests may only create OPTIMISTIC read-write transactions: set concurrency_mode to OPTIMISTIC."}
	// errTooManyTxns and errTooManyLocks are over the bounds of ADR-024 §6.3. Our texts.
	errTooManyTxns  = &Error{Aborted, "Too many open transactions on this database. Please try again."}
	errTooManyLocks = &Error{Aborted, "The transaction reads or writes too many documents. Please try again with fewer."}
)

// TxnStats counts what the transactions and the lock manager did since the store was made.
type TxnStats struct {
	// Waits is lock requests that queued; Timeouts those that gave up at WaitTimeout.
	Waits, Timeouts int64
	// Wounds is transactions wounded by an older waiter.
	Wounds int64
	// Expired is transactions ended at MaxLifetime.
	Expired int64
	// CapRejects is Begins refused for MaxOpen or MaxOpenUnlocked.
	CapRejects int64
	// Conflicts is optimistic commits aborted because what they read changed.
	Conflicts int64
	// LockViolations is pessimistic commits that found a document they read changed under
	// their lock: a bug in the lock manager, or a second process (ADR-024 §6.4, §7).
	LockViolations int64
}

type txnCounters struct {
	waits, timeouts, wounds, expired, capRejects, conflicts, violations atomic.Int64
}

type txnMode uint8

const (
	txnReadOnly txnMode = iota + 1
	txnOptimistic
	txnPessimistic
)

// readVersion is a document as a transaction read it.
type readVersion struct {
	path       Path
	exists     bool
	updateTime time.Time
}

// same reports whether the document is still as read. A version is its update_time, which is
// unique in a database: every commit's commit_time is above the last one's (ADR-024 §2.1), so it
// identifies the commit that wrote the document as its update_seq does.
func (v readVersion) same(exists bool, updateTime time.Time) bool {
	return v.exists == exists && (!exists || v.updateTime.Equal(updateTime))
}

// txn is an open transaction, or the record of an ended one.
type txn struct {
	num      uint64
	db       DB
	mode     txnMode
	issued   time.Time
	readTime time.Time  // read-only: the snapshot
	table    *lockTable // pessimistic: its locks
	lt       *lockTxn
	age      uint64 // optimistic: the age its commit's locks take; 0 for a new one

	mu        sync.Mutex
	ended     bool
	endedAt   time.Time
	committed bool                   // the commit started: reads and Rollback no longer apply
	reads     map[string]readVersion // by stored name key
	lists     []listRead             // optimistic
}

// transactions is the registry of a store's transactions.
type transactions struct {
	cfg   *TxnConfig
	stats *txnCounters
	locks *lockManager
	key   []byte

	mu    sync.Mutex
	nonce [8]byte
	next  uint64
	all   map[uint64]*txn
	open  map[dbKey]*openCounts
}

type openCounts struct{ locked, unlocked int }

func newTransactions(cfg TxnConfig) *transactions {
	cfg.defaults()
	key := cfg.Key
	if len(key) == 0 {
		key = make([]byte, 32)
		_, _ = rand.Read(key)
	}
	stats := &txnCounters{}
	r := &transactions{cfg: &cfg, stats: stats, key: key, all: map[uint64]*txn{}, open: map[dbKey]*openCounts{}}
	r.locks = newLockManager(r.cfg, stats)
	_, _ = rand.Read(r.nonce[:])
	return r
}

// The id (ADR-024 §6.2, ADR-025 §7.5): a version byte, the boot nonce, the transaction's number
// in this process and the time it was issued, then an HMAC-SHA-256 (cut to 16 bytes) of those and
// of the project, the database and the principal.
const (
	txnIDVersion = 1
	txnIDBody    = 1 + 8 + 8 + 8
	txnIDLen     = txnIDBody + 16
)

func (r *transactions) mac(body []byte, db DB, by Principal) []byte {
	m := hmac.New(sha256.New, r.key)
	m.Write(body)
	var b []byte
	b = binary.BigEndian.AppendUint64(b, uint64(db.Project))
	b = binary.AppendUvarint(b, uint64(len(db.ID)))
	b = append(b, db.ID...)
	if by.Admin {
		b = append(b, 1)
	} else {
		b = append(b, 0)
	}
	b = binary.AppendUvarint(b, uint64(len(by.ID)))
	b = append(b, by.ID...)
	m.Write(b)
	return m.Sum(nil)[:txnIDLen-txnIDBody]
}

func (r *transactions) issue(nonce [8]byte, num uint64, issued time.Time, db DB, by Principal) []byte {
	id := make([]byte, 0, txnIDLen)
	id = append(id, txnIDVersion)
	id = append(id, nonce[:]...)
	id = binary.BigEndian.AppendUint64(id, num)
	id = binary.BigEndian.AppendUint64(id, uint64(issued.UnixMicro()))
	return append(id, r.mac(id, db, by)...)
}

// lookup returns the transaction ref names in db: ErrInvalidTransaction when the id is not
// authentic for db and its principal, nil and no error when it is authentic but unknown here.
func (r *transactions) lookup(db DB, ref TransactionRef) (*txn, error) {
	id := ref.ID
	if len(id) != txnIDLen || id[0] != txnIDVersion || !hmac.Equal(id[txnIDBody:], r.mac(id[:txnIDBody], db, ref.By)) {
		return nil, ErrInvalidTransaction
	}
	num := binary.BigEndian.Uint64(id[9:17])
	r.mu.Lock()
	defer r.mu.Unlock()
	if [8]byte(id[1:9]) != r.nonce {
		return nil, nil
	}
	return r.all[num], nil
}

// begin registers a new transaction.
func (r *transactions) begin(db DB, by Principal, mode txnMode, readTime time.Time, age uint64) ([]byte, error) {
	now := r.cfg.now()
	tx := &txn{db: db, mode: mode, issued: now, readTime: readTime, reads: map[string]readVersion{}}
	switch mode {
	case txnPessimistic:
		tx.table = r.locks.table(db)
		tx.lt = tx.table.begin(age, false)
	case txnOptimistic:
		tx.age = age
	}
	k := dbKey{db.Project, db.ID}
	r.mu.Lock()
	c := r.open[k]
	if c == nil {
		c = &openCounts{}
		r.open[k] = c
	}
	if mode == txnPessimistic && c.locked >= r.cfg.MaxOpen || mode != txnPessimistic && c.unlocked >= r.cfg.MaxOpenUnlocked {
		r.mu.Unlock()
		if tx.lt != nil {
			tx.table.release(tx.lt)
		}
		r.stats.capRejects.Add(1)
		return nil, errTooManyTxns
	}
	if mode == txnPessimistic {
		c.locked++
	} else {
		c.unlocked++
	}
	r.next++
	tx.num = r.next
	r.all[tx.num] = tx
	nonce := r.nonce
	r.mu.Unlock()
	return r.issue(nonce, tx.num, now, db, by), nil
}

// retire ends the attempt a retry names, if it is still open, and returns its age (0 when it
// never asked for a lock, or is unknown here).
func (r *transactions) retire(db DB, by Principal, id []byte) (uint64, error) {
	tx, err := r.lookup(db, TransactionRef{ID: id, By: by})
	if err != nil || tx == nil {
		return 0, err
	}
	tx.mu.Lock()
	defer tx.mu.Unlock()
	age := tx.age
	if tx.lt != nil {
		age = tx.table.age(tx.lt)
	}
	if !tx.committed {
		r.finishLocked(tx)
	}
	return age, nil
}

// liveLocked reports whether tx may still be used, ending it if it is past its lifetime or its
// lock manager side ended (wounded, timed out). The call that finds it so answers why; later
// calls find it ended.
func (r *transactions) liveLocked(tx *txn) error {
	if tx.ended || tx.committed {
		return ErrTransactionExpired
	}
	if r.cfg.now().Sub(tx.issued) > r.cfg.MaxLifetime {
		r.stats.expired.Add(1)
		r.finishLocked(tx)
		return ErrTransactionExpired
	}
	if tx.lt != nil {
		if err := tx.table.state(tx.lt); err != nil {
			r.finishLocked(tx)
			return err
		}
	}
	return nil
}

// use returns the open transaction ref names, for a read. done must follow.
func (r *transactions) use(db DB, ref TransactionRef) (*txn, error) {
	tx, err := r.lookup(db, ref)
	if err != nil {
		return nil, err
	}
	if tx == nil {
		return nil, ErrTransactionExpired
	}
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if err := r.liveLocked(tx); err != nil {
		return nil, err
	}
	if tx.lt != nil {
		if err := tx.table.enter(tx.lt); err != nil {
			r.finishLocked(tx)
			return nil, err
		}
	}
	return tx, nil
}

// done ends a call use began.
func (r *transactions) done(tx *txn) {
	if tx.lt != nil {
		tx.table.leave(tx.lt)
	}
}

// failed ends tx when a lock request of its call ended its lock manager side (wounded, timed
// out), so that later calls find it ended; it returns err.
func (r *transactions) failed(tx *txn, err error) error {
	if tx.lt != nil && tx.table.state(tx.lt) != nil {
		r.finish(tx)
	}
	return err
}

// record adds the documents a read returned to the transaction's read set, and the listing that
// returned them if any, and reports whether the transaction is still live: a pessimistic one
// wounded during the read answers why. A document keeps the version first read.
func (r *transactions) record(tx *txn, docs []Document, list *listRead) error {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if err := r.liveLocked(tx); err != nil {
		return err
	}
	for _, d := range docs {
		k := string(storedKey(nameKey(d.Path)))
		if _, ok := tx.reads[k]; !ok {
			tx.reads[k] = readVersion{path: d.Path, exists: d.Exists(), updateTime: d.UpdateTime}
		}
	}
	if list != nil {
		tx.lists = append(tx.lists, *list)
	}
	if tx.mode == txnOptimistic && len(tx.reads)+len(tx.lists) > r.cfg.MaxLocks {
		r.finishLocked(tx)
		return errTooManyLocks
	}
	return nil
}

// startCommit marks tx as committing: no read or Rollback applies from here. It returns the
// versions read, for the commit to check.
func (r *transactions) startCommit(tx *txn) (map[string]readVersion, []listRead, error) {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if err := r.liveLocked(tx); err != nil {
		return nil, nil, err
	}
	tx.committed = true
	return maps.Clone(tx.reads), tx.lists, nil
}

// finish ends tx and frees its locks.
func (r *transactions) finish(tx *txn) {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	r.finishLocked(tx)
}

func (r *transactions) finishLocked(tx *txn) {
	if tx.ended {
		return
	}
	tx.ended, tx.endedAt = true, r.cfg.now()
	tx.reads, tx.lists = nil, nil
	if tx.lt != nil {
		tx.table.release(tx.lt)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.all[tx.num] != tx {
		return // dropped
	}
	c := r.open[dbKey{tx.db.Project, tx.db.ID}]
	if tx.mode == txnPessimistic {
		c.locked--
	} else {
		c.unlocked--
	}
}

// drop ends every transaction and makes every id issued so far unknown (authentic, so ABORTED):
// the service calls it when this process loses the single-instance lock (ADR-024 §7).
func (r *transactions) drop() {
	r.mu.Lock()
	all := r.all
	r.all, r.open = map[uint64]*txn{}, map[dbKey]*openCounts{}
	_, _ = rand.Read(r.nonce[:])
	r.mu.Unlock()
	for _, tx := range all {
		r.finish(tx)
	}
}

// BeginTransaction implements Transactions.
func (s *PGStore) BeginTransaction(ctx context.Context, db DB, by Principal, o TransactionOptions) ([]byte, error) {
	if err := s.serving(); err != nil {
		return nil, err
	}
	if o.ReadOnly {
		readTime, err := s.snapshot(ctx, db, o.ReadTime)
		if err != nil {
			return nil, err
		}
		return s.txns.begin(db, by, txnReadOnly, readTime, 0)
	}
	mode := txnPessimistic
	switch {
	case !by.Admin && o.Concurrency != ConcurrencyOptimistic:
		return nil, ErrOptimisticRequired
	case o.Concurrency == ConcurrencyOptimistic:
		mode = txnOptimistic
	}
	var age uint64
	if o.Retry != nil {
		var err error
		if age, err = s.txns.retire(db, by, o.Retry); err != nil {
			return nil, err
		}
	}
	// The database must exist ('(default)' is created): production answers NOT_FOUND at Begin.
	if _, err := s.Database(ctx, db); err != nil {
		return nil, err
	}
	return s.txns.begin(db, by, mode, time.Time{}, age)
}

// Rollback implements Transactions.
func (s *PGStore) Rollback(ctx context.Context, db DB, t TransactionRef) error {
	if err := s.serving(); err != nil {
		return err
	}
	tx, err := s.txns.lookup(db, t)
	if err != nil || tx == nil {
		return err
	}
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if !tx.committed {
		s.txns.finishLocked(tx)
	}
	return nil
}

// CommitTransaction implements Transactions.
func (s *PGStore) CommitTransaction(ctx context.Context, db DB, t TransactionRef, writes []Write) (CommitResult, error) {
	if err := s.serving(); err != nil {
		return CommitResult{}, err
	}
	tx, err := s.txns.lookup(db, t)
	if err != nil {
		return CommitResult{}, err
	}
	if tx == nil {
		return CommitResult{}, ErrTransactionExpired
	}
	reads, lists, err := s.txns.startCommit(tx)
	if err != nil {
		return CommitResult{}, err
	}
	defer s.txns.finish(tx)
	if tx.mode == txnReadOnly && len(writes) > 0 {
		return CommitResult{}, ErrReadOnlyWrite
	}
	return s.commit(ctx, db, writes, &commitTxn{tx: tx, reads: reads, lists: lists})
}

// txnBatchGet is BatchGet in a transaction (ReadOptions.Transaction).
func (s *PGStore) txnBatchGet(ctx context.Context, db DB, paths []Path, o ReadOptions) ([]Document, time.Time, error) {
	if !o.ReadTime.IsZero() {
		return nil, time.Time{}, errTxnReadTime
	}
	tx, err := s.txns.use(db, *o.Transaction)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer s.txns.done(tx)
	switch tx.mode {
	case txnReadOnly:
		return s.BatchGet(ctx, db, paths, ReadOptions{ReadTime: tx.readTime})
	case txnPessimistic:
		if err := tx.table.acquireAll(ctx, tx.lt, readLocks(paths)); err != nil {
			return nil, time.Time{}, s.txns.failed(tx, err)
		}
	}
	docs, readTime, err := s.BatchGet(ctx, db, paths, ReadOptions{})
	if err != nil {
		return nil, time.Time{}, err
	}
	if err := s.txns.record(tx, docs, nil); err != nil {
		return nil, time.Time{}, err
	}
	return docs, readTime, nil
}

// txnList is ListDocuments in a transaction (ListRequest.Transaction).
func (s *PGStore) txnList(ctx context.Context, db DB, r ListRequest) (ListResult, error) {
	ref := *r.Transaction
	r.Transaction = nil
	if !r.ReadTime.IsZero() {
		return ListResult{}, errTxnReadTime
	}
	tx, err := s.txns.use(db, ref)
	if err != nil {
		return ListResult{}, err
	}
	defer s.txns.done(tx)
	switch tx.mode {
	case txnReadOnly:
		r.ReadTime = tx.readTime
		return s.ListDocuments(ctx, db, r)
	case txnPessimistic:
		if err := tx.table.acquire(ctx, tx.lt, listLock(r), lockShared); err != nil {
			return ListResult{}, s.txns.failed(tx, err)
		}
	}
	res, err := s.ListDocuments(ctx, db, r)
	if err != nil {
		return ListResult{}, err
	}
	var list *listRead
	if tx.mode == txnOptimistic {
		list = &listRead{req: r, lock: listLock(r), docs: versions(res.Documents)}
	}
	if err := s.txns.record(tx, res.Documents, list); err != nil {
		return ListResult{}, err
	}
	return res, nil
}

// commitTxn is what a transaction's commit checks and locks: the transaction, and what it read.
type commitTxn struct {
	tx    *txn
	reads map[string]readVersion
	lists []listRead
}

// lockCommit takes the lock manager's locks of a commit (ADR-024 §2.1 step 1) and checks what a
// transaction read, so that no other commit changes it until this one's ends; release frees the
// locks. Without a transaction (c nil) the commit's exclusive locks are its own, short-lived; they
// wait for other transactions' locks like any (f0-contention s6b).
func (s *PGStore) lockCommit(ctx context.Context, db DB, p *plan, c *commitTxn) (release func(), err error) {
	switch {
	case c == nil:
		return s.lockWrites(ctx, db, p, 0, nil, nil)
	case c.tx.mode == txnPessimistic:
		return s.lockPessimistic(ctx, p, c)
	case c.tx.mode == txnOptimistic:
		return s.lockOptimistic(ctx, db, p, c)
	}
	return func() {}, nil // read-only: no writes
}

// writeLocks returns the locks a commit's writes take: intent on the database, the collection
// group and the collection of each document, then exclusive on the document, in stored key
// order (ADR-024 §6.1).
func writeLocks(p *plan) []lockSpec {
	specs := make([]lockSpec, 0, 4*len(p.docs))
	if len(p.docs) > 0 {
		specs = append(specs, lockSpec{databaseLock, lockIntent})
	}
	for _, d := range p.docs {
		specs = append(specs, lockSpec{groupLock(d.keys), lockIntent}, lockSpec{collectionLock(d.keys), lockIntent})
	}
	for _, d := range p.docs {
		specs = append(specs, lockSpec{docLock(d.keys), lockExclusive})
	}
	return specs
}

// lockWrites takes a commit's write locks, after the locks of more, as a transaction of its own,
// of age (0: a new one). check, when set, runs once they are held, before the commit is marked
// committing.
//
// Such a commit has done nothing before it is marked committing: when an older transaction
// wounds it, it frees its locks and asks for them again, keeping its age, so that it only grows
// older; the client never sees that wound. Every wait ends WaitTimeout after the first request,
// with ErrContention (f0-contention s6b).
func (s *PGStore) lockWrites(ctx context.Context, db DB, p *plan, age uint64, more []lockSpec, check func() error) (func(), error) {
	specs := append(more, writeLocks(p)...)
	if len(specs) == 0 && check == nil {
		return func() {}, nil
	}
	table := s.txns.locks.table(db)
	waitUntil := time.Now().Add(s.txns.cfg.WaitTimeout)
	for {
		lt := table.begin(age, true)
		lt.waitUntil = waitUntil
		release := func() { table.release(lt) }
		err := table.acquireAll(ctx, lt, specs)
		if err == nil && check != nil {
			err = check()
		}
		if err == nil {
			err = table.markCommitting(lt)
		}
		if err == nil {
			return release, nil
		}
		age = table.age(lt)
		release()
		if !errors.Is(err, ErrWounded) {
			return nil, err
		}
		if !time.Now().Before(waitUntil) {
			return nil, ErrContention
		}
	}
}

// verifyWritten is the check of a commit's written documents that the transaction read against
// their rows as the commit locked them (plan.verify, ADR-024 §6.4): a document whose version is
// not the one read fails the commit with onChange's error.
func verifyWritten(p *plan, reads map[string]readVersion, onChange func() error) func(map[string]*lockedDoc) error {
	return func(locked map[string]*lockedDoc) error {
		for _, d := range p.docs {
			v, ok := reads[string(d.keys.stored)]
			if !ok {
				continue
			}
			row := locked[string(d.keys.stored)]
			var updateTime time.Time
			if row != nil {
				updateTime = row.updateTime
			}
			if !v.same(row != nil, updateTime) {
				return onChange()
			}
		}
		return nil
	}
}

// TransactionStats returns the transactions' counters.
func (s *PGStore) TransactionStats() TxnStats {
	c := s.txns.stats
	return TxnStats{
		Waits: c.waits.Load(), Timeouts: c.timeouts.Load(), Wounds: c.wounds.Load(), Expired: c.expired.Load(),
		CapRejects: c.capRejects.Load(), Conflicts: c.conflicts.Load(), LockViolations: c.violations.Load(),
	}
}

// DropTransactions ends every transaction and makes every id issued so far unknown: the service
// calls it when this process loses the single-instance lock (InstanceOptions.OnLost).
func (s *PGStore) DropTransactions() { s.txns.drop() }

// The transports never send these: the protos have no room for them.
var (
	// errTxnReadTime is a read in a transaction given a read time too (a oneof).
	errTxnReadTime = errors.New("documents: a read in a transaction takes no read time")
	// errTxnListCollectionIDs is ListCollectionIds in a transaction, which it has no field for.
	errTxnListCollectionIDs = errors.New("documents: ListCollectionIDs takes no transaction")
)
