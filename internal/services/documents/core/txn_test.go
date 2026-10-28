package core

import (
	"bytes"
	"context"
	"errors"
	"math/rand/v2"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arumadev/aruma/internal/services/documents/value"
)

var (
	admin   = Principal{Admin: true, ID: "firebase-adminsdk@chatapp-8ae21.iam.gserviceaccount.com"}
	endUser = Principal{ID: "uid-1"}
)

func begin(t *testing.T, s *PGStore, by Principal, o TransactionOptions) TransactionRef {
	t.Helper()
	id, err := s.BeginTransaction(t.Context(), testDB, by, o)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	return TransactionRef{ID: id, By: by}
}

func in(ref TransactionRef) ReadOptions { return ReadOptions{Transaction: &ref} }

func intField(t *testing.T, d Document, f string) int64 {
	t.Helper()
	v, ok := d.Fields.Get(f)
	if !ok {
		t.Fatalf("%s has no %s", d.Path, f)
	}
	return v.IntValue()
}

// An admin's transaction reads, then commits; the id is spent after (f0-errors.json
// txn_reused.commit_second; f0-contention s5).
func TestPessimisticReadThenCommit(t *testing.T) {
	s, _ := newStore(t, Options{})
	ctx := t.Context()
	mustCommit(t, s, set("acct/a", doc("n", value.Int(1))))
	ref := begin(t, s, admin, TransactionOptions{})
	d, _, err := s.Get(ctx, testDB, ParsePath("acct/a"), in(ref))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Get(ctx, testDB, ParsePath("acct/none"), in(ref)); !isCode(err, NotFound) {
		t.Fatalf("missing document in a transaction: %v", err)
	}
	res, err := s.CommitTransaction(ctx, testDB, ref, []Write{set("acct/a", doc("n", value.Int(intField(t, d, "n")+1)))})
	if err != nil || len(res.Results) != 1 {
		t.Fatalf("commit: %+v %v", res, err)
	}
	if _, err := s.CommitTransaction(ctx, testDB, ref, nil); !errors.Is(err, ErrTransactionExpired) {
		t.Fatalf("commit again: %v", err)
	}
	if _, _, err := s.Get(ctx, testDB, ParsePath("acct/a"), in(ref)); !errors.Is(err, ErrTransactionExpired) {
		t.Fatalf("read after the commit: %v", err)
	}
	if err := s.Rollback(ctx, testDB, ref); err != nil {
		t.Fatalf("rollback after the commit: %v", err)
	}
	if d, _, _ := s.Get(ctx, testDB, ParsePath("acct/a"), ReadOptions{}); intField(t, d, "n") != 2 {
		t.Fatal("the commit did not apply")
	}
	if n, locks := s.txns.locks.table(testDB).open(); n != 0 || locks != 0 {
		t.Fatalf("%d lock transactions, %d locks left", n, locks)
	}
}

func isCode(err error, c Code) bool {
	e, ok := errors.AsType[*Error](err)
	return ok && e.Code == c
}

// A forged or foreign id is "Invalid transaction."; an authentic one this process no longer
// knows is ABORTED (ADR-025 §7.5).
func TestTransactionIDs(t *testing.T) {
	s, _ := newStore(t, Options{})
	ctx := t.Context()
	ref := begin(t, s, admin, TransactionOptions{})
	forged := func(id []byte, by Principal) TransactionRef { return TransactionRef{ID: id, By: by} }
	flipped := bytes.Clone(ref.ID)
	flipped[12] ^= 1
	other := DB{Project: testDB.Project + 1, ProjectID: "other", ID: DefaultDatabase}
	for name, call := range map[string]func() error{
		"garbage": func() error { return s.Rollback(ctx, testDB, forged([]byte("aruma-ref-no-such-transaction"), admin)) },
		"flipped": func() error { _, err := s.CommitTransaction(ctx, testDB, forged(flipped, admin), nil); return err },
		"other principal": func() error {
			_, _, err := s.Get(ctx, testDB, ParsePath("c/d"), in(forged(ref.ID, endUser)))
			return err
		},
		"other project": func() error {
			_, err := s.ListDocuments(ctx, other, ListRequest{CollectionID: "c", ReadOptions: in(ref)})
			return err
		},
		"empty": func() error { _, err := s.CommitTransaction(ctx, testDB, forged(nil, admin), nil); return err },
	} {
		if err := call(); !errors.Is(err, ErrInvalidTransaction) {
			t.Errorf("%s: %v", name, err)
		}
	}
	wantError(t, ErrInvalidTransaction, InvalidArgument, "Invalid transaction.")
	wantError(t, ErrTransactionExpired, Aborted, "The referenced transaction has expired or is no longer valid.")

	// The process lost its instance lock, or restarted with the same key: the id is authentic
	// and unknown.
	s.DropTransactions()
	if _, err := s.CommitTransaction(ctx, testDB, ref, nil); !errors.Is(err, ErrTransactionExpired) {
		t.Fatalf("after drop: %v", err)
	}
	if err := s.Rollback(ctx, testDB, ref); err != nil {
		t.Fatalf("rollback of an unknown transaction: %v", err)
	}
}

// A transaction ends 270 s after Begin, not when idle (f0-contention s5).
func TestTransactionExpiry(t *testing.T) {
	clk := newClock()
	s, _ := newStore(t, Options{Transactions: TxnConfig{Now: clk.Now}})
	ctx := t.Context()
	mustCommit(t, s, set("exp/a", doc()))
	idle := begin(t, s, admin, TransactionOptions{})
	if _, _, err := s.Get(ctx, testDB, ParsePath("exp/a"), in(idle)); err != nil {
		t.Fatal(err)
	}
	clk.advance(70 * time.Second)
	if _, err := s.CommitTransaction(ctx, testDB, idle, []Write{set("exp/a", doc("n", value.Int(1)))}); err != nil {
		t.Fatalf("commit after 70 s idle: %v", err)
	}

	old := begin(t, s, admin, TransactionOptions{})
	if _, _, err := s.Get(ctx, testDB, ParsePath("exp/a"), in(old)); err != nil {
		t.Fatal(err)
	}
	clk.advance(271 * time.Second)
	s.SweepTransactions()
	if s.TransactionStats().Expired != 1 {
		t.Fatalf("stats %+v", s.TransactionStats())
	}
	if n, locks := s.txns.locks.table(testDB).open(); n != 0 || locks != 0 {
		t.Fatalf("the expired transaction kept %d locks", locks)
	}
	if _, err := s.CommitTransaction(ctx, testDB, old, nil); !errors.Is(err, ErrTransactionExpired) {
		t.Fatalf("commit after 271 s: %v", err)
	}
	if err := s.Rollback(ctx, testDB, old); err != nil {
		t.Fatalf("rollback of an expired transaction: %v", err)
	}
	// Production forgets it by 300 s after Begin: its id is then as one that never existed, on
	// every call (f2-transactions.json lifetime idle_read_write).
	clk.advance(28 * time.Second)
	s.SweepTransactions()
	if _, err := s.CommitTransaction(ctx, testDB, old, nil); !errors.Is(err, ErrTransactionExpired) {
		t.Fatalf("commit at 299 s: %v", err)
	}
	clk.advance(2 * time.Second)
	s.SweepTransactions()
	if n := len(s.txns.all); n != 0 {
		t.Fatalf("%d records left", n)
	}
	if _, err := s.CommitTransaction(ctx, testDB, old, nil); !errors.Is(err, ErrInvalidTransaction) {
		t.Fatalf("commit at 301 s: %v", err)
	}
	if _, _, err := s.Get(ctx, testDB, ParsePath("exp/a"), in(old)); !errors.Is(err, ErrInvalidTransaction) {
		t.Fatalf("read at 301 s: %v", err)
	}
	if err := s.Rollback(ctx, testDB, old); !errors.Is(err, ErrInvalidTransaction) {
		t.Fatalf("rollback at 301 s: %v", err)
	}
	if _, err := s.BeginTransaction(ctx, testDB, admin, TransactionOptions{Retry: old.ID}); !errors.Is(err, ErrInvalidTransaction) {
		t.Fatalf("retry at 301 s: %v", err)
	}
}

// f0-contention s1 on Postgres: both read D; the younger's commit waits; the older commits and
// the younger is wounded. The older's write is the last.
func TestConflictingPessimisticWriters(t *testing.T) {
	s, _ := newStore(t, Options{})
	ctx := t.Context()
	d := ParsePath("txn/D")
	mustCommit(t, s, set("txn/D", doc("w", value.String("setup"))))
	t1 := begin(t, s, admin, TransactionOptions{})
	t2 := begin(t, s, admin, TransactionOptions{})
	for _, ref := range []TransactionRef{t1, t2} {
		if _, _, err := s.Get(ctx, testDB, d, in(ref)); err != nil {
			t.Fatal(err)
		}
	}
	t2Commit := make(chan error, 1)
	go func() {
		_, err := s.CommitTransaction(ctx, testDB, t2, []Write{set("txn/D", doc("w", value.String("T2")))})
		t2Commit <- err
	}()
	pending(t, t2Commit)
	if _, err := s.CommitTransaction(ctx, testDB, t1, []Write{set("txn/D", doc("w", value.String("T1")))}); err != nil {
		t.Fatalf("T1: %v", err)
	}
	if err := outcome(t, t2Commit); !errors.Is(err, ErrWounded) {
		t.Fatalf("T2: %v", err)
	}
	got, _, _ := s.Get(ctx, testDB, d, ReadOptions{})
	if w, _ := got.Fields.Get("w"); w.StringValue() != "T1" {
		t.Fatalf("final writer %v", w)
	}
}

// f0-contention s3a: the older's blind write wounds the younger reader, whose next read is ABORTED
// with the wound text, and later calls with the contention one; its rollback succeeds
// (f2-locks.json wounded_transaction).
func TestWoundedReaderLearnsOnNextCall(t *testing.T) {
	s, _ := newStore(t, Options{})
	ctx := t.Context()
	t1 := begin(t, s, admin, TransactionOptions{})
	t2 := begin(t, s, admin, TransactionOptions{})
	if _, _, err := s.BatchGet(ctx, testDB, []Path{ParsePath("txn/E")}, in(t1)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BatchGet(ctx, testDB, []Path{ParsePath("txn/D")}, in(t2)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CommitTransaction(ctx, testDB, t1, []Write{set("txn/D", doc())}); err != nil {
		t.Fatalf("T1: %v", err)
	}
	if _, _, err := s.BatchGet(ctx, testDB, []Path{ParsePath("txn/D")}, in(t2)); !errors.Is(err, ErrWounded) {
		t.Fatalf("T2 read: %v", err)
	}
	if _, _, err := s.BatchGet(ctx, testDB, []Path{ParsePath("txn/D")}, in(t2)); !errors.Is(err, ErrContention) {
		t.Fatalf("T2 read again: %v", err)
	}
	if _, err := s.CommitTransaction(ctx, testDB, t2, nil); !errors.Is(err, ErrContention) {
		t.Fatalf("T2 commit: %v", err)
	}
	if err := s.Rollback(ctx, testDB, t2); err != nil {
		t.Fatalf("T2 rollback: %v", err)
	}
}

// f0-contention s6b: a commit without a transaction waits for a transaction's read lock, and
// gives up at the wait timeout with "Too much contention"; the document is unchanged.
func TestCommitWaitsForReadLock(t *testing.T) {
	s, _ := newStore(t, Options{Transactions: TxnConfig{WaitTimeout: 100 * time.Millisecond}})
	ctx := t.Context()
	mustCommit(t, s, set("txn/D", doc("v", value.Int(1))))
	reader := begin(t, s, admin, TransactionOptions{})
	if _, _, err := s.Get(ctx, testDB, ParsePath("txn/D"), in(reader)); err != nil {
		t.Fatal(err)
	}
	_, err := s.Commit(ctx, testDB, []Write{set("txn/D", doc("v", value.Int(2)))})
	wantError(t, err, Aborted, "Too much contention on these documents. Please try again.")
	// A write elsewhere does not wait.
	mustCommit(t, s, set("txn/E", doc()))
	if err := s.Rollback(ctx, testDB, reader); err != nil {
		t.Fatal(err)
	}
	mustCommit(t, s, set("txn/D", doc("v", value.Int(2))))
}

// A commit without a transaction that an older transaction wounds while it waits has done
// nothing yet: it asks for its locks again, keeping its age, and the client never sees the wound.
func TestPlainCommitRetriesItsWound(t *testing.T) {
	s, _ := newStore(t, Options{})
	ctx := t.Context()
	older := begin(t, s, admin, TransactionOptions{})
	if _, _, err := s.Get(ctx, testDB, ParsePath("w/E"), in(older)); !isCode(err, NotFound) {
		t.Fatal(err)
	}
	// The plain commit takes D, then waits for E, which the older holds.
	plain := make(chan error, 1)
	go func() {
		_, err := s.Commit(ctx, testDB, []Write{set("w/D", doc()), set("w/E", doc())})
		plain <- err
	}()
	pending(t, plain)
	// The older reads D: it wounds the plain commit, which frees D and queues again.
	if _, _, err := s.Get(ctx, testDB, ParsePath("w/D"), in(older)); !isCode(err, NotFound) {
		t.Fatalf("older read: %v", err)
	}
	pending(t, plain)
	if err := s.Rollback(ctx, testDB, older); err != nil {
		t.Fatal(err)
	}
	if err := outcome(t, plain); err != nil {
		t.Fatalf("plain commit: %v", err)
	}
	if s.TransactionStats().Wounds != 1 {
		t.Fatalf("stats %+v", s.TransactionStats())
	}
}

// A plain commit wounded while the rules judge it is judged again, and what the judging counted
// in the usage (CountUsage) counts once: for the attempt that commits.
func TestWoundRetryCountsUsageOnce(t *testing.T) {
	s, _ := newStore(t, Options{})
	ctx := t.Context()
	older := begin(t, s, admin, TransactionOptions{})
	// Its first lock gives it its age (s3b).
	if _, _, err := s.Get(ctx, testDB, ParsePath("u/E"), in(older)); !isCode(err, NotFound) {
		t.Fatal(err)
	}
	var judged, counted atomic.Int64
	read := make(chan error, 1)
	judge := func(ctx context.Context, _ []Proposal, _ time.Time) error {
		CountUsage(ctx, func() { counted.Add(1) })
		if judged.Add(1) > 1 {
			return nil
		}
		// The older reads D, which the commit holds: it wounds the commit, then waits for it.
		go func() {
			_, _, err := s.Get(context.WithoutCancel(ctx), testDB, ParsePath("u/D"), in(older))
			read <- err
		}()
		for s.TransactionStats().Wounds == 0 {
			time.Sleep(time.Millisecond)
		}
		return nil
	}
	plain := make(chan error, 1)
	go func() {
		_, err := s.Commit(WithAuthorizer(ctx, judge), testDB, []Write{set("u/D", doc())})
		plain <- err
	}()
	if err := outcome(t, read); !isCode(err, NotFound) {
		t.Fatalf("older read: %v", err)
	}
	pending(t, plain)
	if err := s.Rollback(ctx, testDB, older); err != nil {
		t.Fatal(err)
	}
	if err := outcome(t, plain); err != nil {
		t.Fatalf("plain commit: %v", err)
	}
	if judged.Load() != 2 || counted.Load() != 1 {
		t.Errorf("judged %d times, counted %d; want 2 and 1", judged.Load(), counted.Load())
	}
}

// A listing in a pessimistic transaction locks its collection: a new document there waits,
// another collection's does not (ADR-024 §6.1, coarse-query-locks).
func TestPessimisticListLocksItsCollection(t *testing.T) {
	s, _ := newStore(t, Options{Transactions: TxnConfig{WaitTimeout: 100 * time.Millisecond}})
	ctx := t.Context()
	mustCommit(t, s, set("c/A/sub/1", doc()))
	ref := begin(t, s, admin, TransactionOptions{})
	res, err := s.ListDocuments(ctx, testDB, ListRequest{Parent: ParsePath("c/A"), CollectionID: "sub", ReadOptions: in(ref)})
	if err != nil || len(res.Documents) != 1 {
		t.Fatalf("list: %v %v", res, err)
	}
	mustCommit(t, s, set("c/B/sub/x", doc()))
	if _, err := s.Commit(ctx, testDB, []Write{set("c/A/sub/2", doc())}); !errors.Is(err, ErrContention) {
		t.Fatalf("a phantom: %v", err)
	}
	// The transaction itself may write there.
	if _, err := s.CommitTransaction(ctx, testDB, ref, []Write{set("c/A/sub/2", doc())}); err != nil {
		t.Fatal(err)
	}
}

// A read-only transaction reads one snapshot, at its read_time or else at its first read, and
// takes no write. A commit between its Begin and its first read is seen, as production's query
// in a read-only transaction sees one (f2-transactions.json read.run_query_read_only); one after
// its first read is not.
func TestReadOnlyTransaction(t *testing.T) {
	s, _ := newStore(t, Options{})
	ctx := t.Context()
	first := mustCommit(t, s, set("ro/a", doc("n", value.Int(1))))
	mustCommit(t, s, set("ro/a", doc("n", value.Int(2))))
	now := begin(t, s, endUser, TransactionOptions{ReadOnly: true})
	then := begin(t, s, endUser, TransactionOptions{ReadOnly: true, ReadTime: first.CommitTime})
	mustCommit(t, s, set("ro/a", doc("n", value.Int(3))), set("ro/b", doc()))
	type want struct {
		n    int64
		docs int
	}
	wants := map[*TransactionRef]want{&now: {3, 2}, &then: {1, 1}}
	readTimes := map[*TransactionRef]time.Time{}
	for ref, want := range wants {
		d, readTime, err := s.Get(ctx, testDB, ParsePath("ro/a"), in(*ref))
		if err != nil || intField(t, d, "n") != want.n {
			t.Fatalf("first read: %v %v, want n=%d", d, err, want.n)
		}
		readTimes[ref] = readTime
	}
	mustCommit(t, s, set("ro/a", doc("n", value.Int(4))), set("ro/c", doc()))
	for ref, want := range wants {
		d, readTime, err := s.Get(ctx, testDB, ParsePath("ro/a"), in(*ref))
		if err != nil || intField(t, d, "n") != want.n || !readTime.Equal(readTimes[ref]) {
			t.Fatalf("a later read: %v at %v %v, want n=%d at %v", d, readTime, err, want.n, readTimes[ref])
		}
		res, err := s.ListDocuments(ctx, testDB, ListRequest{CollectionID: "ro", ReadOptions: in(*ref)})
		if err != nil || len(res.Documents) != want.docs || !res.ReadTime.Equal(readTime) {
			t.Fatalf("list: %v %v", paths(res.Documents), err)
		}
		res, err = s.Scan(ctx, testDB, ScanRequest{CollectionID: "ro", ReadOptions: in(*ref)})
		if err != nil || len(res.Documents) != want.docs || !res.ReadTime.Equal(readTime) {
			t.Fatalf("scan: %v %v", paths(res.Documents), err)
		}
	}
	_, err := s.CommitTransaction(ctx, testDB, now, []Write{set("ro/d", doc())})
	wantError(t, err, InvalidArgument, ErrReadOnlyWrite.Message)
	// After its refused write, the transaction's expired text comes as INVALID_ARGUMENT
	// (f2-transactions.json ro.commit_empty_after_refused_write).
	_, err = s.CommitTransaction(ctx, testDB, now, nil)
	wantError(t, err, InvalidArgument, ErrTransactionExpired.Message)
	// A read-only transaction cannot be retried (begin.retry_of_read_only).
	_, err = s.BeginTransaction(ctx, testDB, endUser, TransactionOptions{Concurrency: ConcurrencyOptimistic, Retry: then.ID})
	wantError(t, err, InvalidArgument, "Cannot retry a read-only transaction")
	if res, err := s.CommitTransaction(ctx, testDB, then, nil); err != nil || res.CommitTime.IsZero() {
		t.Fatalf("an empty commit: %+v %v", res, err)
	}
	if _, err := s.BeginTransaction(ctx, testDB, admin, TransactionOptions{ReadOnly: true, ReadTime: time.Now().Add(time.Hour)}); !errors.Is(err, ErrReadTimeFuture) {
		t.Fatalf("future read time: %v", err)
	}
	if _, err := s.BeginTransaction(ctx, testDB, admin, TransactionOptions{ReadOnly: true, ReadTime: time.Now().Add(-2 * time.Hour)}); !errors.Is(err, ErrReadTimeTooOld) {
		t.Fatalf("old read time: %v", err)
	}
	if n, _ := s.txns.locks.table(testDB).open(); n != 0 {
		t.Fatal("a read-only transaction took locks")
	}
}

// An end user's read-write transaction must ask for OPTIMISTIC; an admin's may.
func TestModelByPrincipal(t *testing.T) {
	s, _ := newStore(t, Options{})
	ctx := t.Context()
	for _, c := range []Concurrency{ConcurrencyDefault, ConcurrencyPessimistic} {
		if _, err := s.BeginTransaction(ctx, testDB, endUser, TransactionOptions{Concurrency: c}); !errors.Is(err, ErrOptimisticRequired) {
			t.Errorf("end user, mode %d: %v", c, err)
		}
	}
	cases := []struct {
		by   Principal
		c    Concurrency
		mode txnMode
	}{
		{endUser, ConcurrencyOptimistic, txnOptimistic},
		{admin, ConcurrencyDefault, txnPessimistic},
		{admin, ConcurrencyPessimistic, txnPessimistic},
		{admin, ConcurrencyOptimistic, txnOptimistic},
	}
	for _, c := range cases {
		ref := begin(t, s, c.by, TransactionOptions{Concurrency: c.c})
		tx, _ := s.txns.lookup(testDB, ref)
		if tx.mode != c.mode {
			t.Errorf("%+v %d: mode %d", c.by, c.c, tx.mode)
		}
	}
	if _, err := s.BeginTransaction(ctx, DB{Project: testDB.Project, ProjectID: "chatapp-8ae21", ID: "named"}, admin, TransactionOptions{}); err == nil {
		t.Error("begin on a database that does not exist")
	}
}

// An optimistic transaction commits when nothing it read changed, and is ABORTED when a document
// it read, or a listing it made, did.
func TestOptimistic(t *testing.T) {
	s, _ := newStore(t, Options{})
	ctx := t.Context()
	opt := TransactionOptions{Concurrency: ConcurrencyOptimistic}
	mustCommit(t, s, set("opt/a", doc("n", value.Int(1))), set("opt/b", doc("n", value.Int(1))))

	ok := begin(t, s, endUser, opt)
	if _, _, err := s.BatchGet(ctx, testDB, []Path{ParsePath("opt/a"), ParsePath("opt/missing")}, in(ok)); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.txns.locks.table(testDB).open(); n != 0 {
		t.Fatal("an optimistic read took locks")
	}
	mustCommit(t, s, set("opt/b", doc("n", value.Int(2)))) // not read
	if _, err := s.CommitTransaction(ctx, testDB, ok, []Write{set("opt/a", doc("n", value.Int(2)))}); err != nil {
		t.Fatalf("unchanged reads: %v", err)
	}

	for name, change := range map[string]Write{
		"written changed":  set("opt/a", doc("n", value.Int(9))),
		"read changed":     set("opt/b", doc("n", value.Int(9))),
		"missing appeared": set("opt/missing", doc()),
		"read deleted":     {Kind: WriteDelete, Path: ParsePath("opt/b")},
	} {
		mustCommit(t, s, set("opt/a", doc("n", value.Int(1))), set("opt/b", doc("n", value.Int(1))), Write{Kind: WriteDelete, Path: ParsePath("opt/missing")})
		ref := begin(t, s, endUser, opt)
		if _, _, err := s.BatchGet(ctx, testDB, []Path{ParsePath("opt/a"), ParsePath("opt/b"), ParsePath("opt/missing")}, in(ref)); err != nil {
			t.Fatal(err)
		}
		mustCommit(t, s, change)
		_, err := s.CommitTransaction(ctx, testDB, ref, []Write{set("opt/a", doc("n", value.Int(5)))})
		if !errors.Is(err, ErrReadsChanged) {
			t.Errorf("%s: %v", name, err)
		}
	}

	// A listing that would now return another document.
	ref := begin(t, s, admin, opt)
	if _, err := s.ListDocuments(ctx, testDB, ListRequest{CollectionID: "opt", ReadOptions: in(ref)}); err != nil {
		t.Fatal(err)
	}
	mustCommit(t, s, set("opt/new", doc()))
	if _, err := s.CommitTransaction(ctx, testDB, ref, []Write{set("other/x", doc())}); !errors.Is(err, ErrReadsChanged) {
		t.Errorf("phantom: %v", err)
	}
	if st := s.TransactionStats(); st.Conflicts != 5 || st.LockViolations != 0 {
		t.Errorf("stats %+v", st)
	}
	if n, locks := s.txns.locks.table(testDB).open(); n != 0 || locks != 0 {
		t.Fatalf("%d lock transactions, %d locks left", n, locks)
	}
}

// retry_transaction keeps the age of the attempt it retries and rolls it back (ADR-024 §6.2).
func TestRetryKeepsAge(t *testing.T) {
	s, _ := newStore(t, Options{})
	ctx := t.Context()
	first := begin(t, s, admin, TransactionOptions{})
	if _, _, err := s.BatchGet(ctx, testDB, []Path{ParsePath("r/a")}, in(first)); err != nil {
		t.Fatal(err)
	}
	younger := begin(t, s, admin, TransactionOptions{})
	if _, _, err := s.BatchGet(ctx, testDB, []Path{ParsePath("r/b")}, in(younger)); err != nil {
		t.Fatal(err)
	}
	tx, _ := s.txns.lookup(testDB, first)
	age := tx.table.age(tx.lt)
	retry := begin(t, s, admin, TransactionOptions{Retry: first.ID})
	if _, _, err := s.Get(ctx, testDB, ParsePath("r/a"), in(first)); !errors.Is(err, ErrTransactionExpired) {
		t.Fatalf("the retried attempt is still open: %v", err)
	}
	rtx, _ := s.txns.lookup(testDB, retry)
	if got := rtx.table.age(rtx.lt); got != age {
		t.Fatalf("retry age %d, want %d", got, age)
	}
	// Being older, the retry wounds the younger holder of b.
	if _, err := s.CommitTransaction(ctx, testDB, retry, []Write{set("r/b", doc())}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Get(ctx, testDB, ParsePath("r/b"), in(younger)); !errors.Is(err, ErrWounded) {
		t.Fatalf("younger: %v", err)
	}
	// A forged retry id is refused; an unknown authentic one starts afresh.
	if _, err := s.BeginTransaction(ctx, testDB, admin, TransactionOptions{Retry: []byte("forged")}); !errors.Is(err, ErrInvalidTransaction) {
		t.Fatal(err)
	}
	s.DropTransactions()
	fresh := begin(t, s, admin, TransactionOptions{Retry: retry.ID})
	ftx, _ := s.txns.lookup(testDB, fresh)
	if ftx.table.age(ftx.lt) != 0 {
		t.Fatal("an unknown retry kept an age")
	}
}

// Over the bound of open pessimistic transactions, Begin is ABORTED; ending one makes room.
func TestMaxOpenTransactions(t *testing.T) {
	s, _ := newStore(t, Options{Transactions: TxnConfig{MaxOpen: 2, MaxOpenUnlocked: 1}})
	ctx := t.Context()
	a := begin(t, s, admin, TransactionOptions{})
	begin(t, s, admin, TransactionOptions{})
	if _, err := s.BeginTransaction(ctx, testDB, admin, TransactionOptions{}); !isCode(err, Aborted) {
		t.Fatal(err)
	}
	begin(t, s, endUser, TransactionOptions{ReadOnly: true})
	if _, err := s.BeginTransaction(ctx, testDB, endUser, TransactionOptions{Concurrency: ConcurrencyOptimistic}); !isCode(err, Aborted) {
		t.Fatal(err)
	}
	if err := s.Rollback(ctx, testDB, a); err != nil {
		t.Fatal(err)
	}
	begin(t, s, admin, TransactionOptions{})
	if s.TransactionStats().CapRejects != 2 {
		t.Fatalf("stats %+v", s.TransactionStats())
	}
}

// One end user's open read-only and optimistic transactions are bounded on their own, so the
// others still begin; an admin, and a caller without a uid, are bounded by the database's alone
// (F2 review M2).
func TestMaxOpenPerUser(t *testing.T) {
	s, _ := newStore(t, Options{Transactions: TxnConfig{MaxOpenPerUser: 2, MaxOpenUnlocked: 6}})
	ctx := t.Context()
	ro := TransactionOptions{ReadOnly: true}
	a := begin(t, s, endUser, ro)
	begin(t, s, endUser, TransactionOptions{Concurrency: ConcurrencyOptimistic})
	if _, err := s.BeginTransaction(ctx, testDB, endUser, ro); !isCode(err, Aborted) {
		t.Fatalf("over the user's bound: %v", err)
	}
	begin(t, s, Principal{ID: "uid-2"}, ro)
	for range 3 {
		begin(t, s, Principal{}, ro)
	}
	if _, err := s.BeginTransaction(ctx, testDB, admin, ro); !isCode(err, Aborted) {
		t.Fatalf("over the database's bound: %v", err)
	}
	if err := s.Rollback(ctx, testDB, a); err != nil {
		t.Fatal(err)
	}
	begin(t, s, endUser, ro)
}

// Past MaxEnded, the oldest records of ended transactions go: their ids answer the expired text
// and a retry of one takes a new age; the records kept still answer as before.
func TestMaxEnded(t *testing.T) {
	s, _ := newStore(t, Options{Transactions: TxnConfig{MaxEnded: 3}})
	ctx := t.Context()
	var refs []TransactionRef
	for range 5 {
		ref := begin(t, s, admin, TransactionOptions{})
		if _, _, err := s.BatchGet(ctx, testDB, []Path{ParsePath("e/a")}, in(ref)); err != nil {
			t.Fatal(err)
		}
		if err := s.Rollback(ctx, testDB, ref); err != nil {
			t.Fatal(err)
		}
		refs = append(refs, ref)
	}
	s.txns.mu.Lock()
	n := len(s.txns.all)
	s.txns.mu.Unlock()
	if n != 3 {
		t.Fatalf("%d records kept", n)
	}
	for _, ref := range refs {
		if _, _, err := s.Get(ctx, testDB, ParsePath("e/a"), in(ref)); !errors.Is(err, ErrTransactionExpired) {
			t.Fatalf("%v", err)
		}
	}
	retry := begin(t, s, admin, TransactionOptions{Retry: refs[0].ID})
	rtx, _ := s.txns.lookup(testDB, retry)
	if rtx.table.age(rtx.lt) != 0 {
		t.Fatal("a forgotten attempt's retry kept an age")
	}
}

// An optimistic transaction's listings count the documents they returned against MaxLocks, as
// its reads do (F2 review M2).
func TestOptimisticListingsBounded(t *testing.T) {
	s, _ := newStore(t, Options{Transactions: TxnConfig{MaxLocks: 5}})
	ctx := t.Context()
	var ws []Write
	for i := range 3 {
		ws = append(ws, set("ol/d"+strconv.Itoa(i), doc()))
	}
	mustCommit(t, s, ws...)
	ref := begin(t, s, endUser, TransactionOptions{Concurrency: ConcurrencyOptimistic})
	list := func() error {
		_, err := s.ListDocuments(ctx, testDB, ListRequest{CollectionID: "ol", PageSize: 10, ReadOptions: in(ref)})
		return err
	}
	// 3 documents, 1 listing, 3 listed.
	if err := list(); !errors.Is(err, errTooManyLocks) {
		t.Fatalf("over MaxLocks: %v", err)
	}
}

// Bank transfers by pessimistic transactions retried on ABORTED, with commits without a
// transaction and optimistic transfers mixed in: the total never changes, and every lock is
// freed. A small randomized stand-in for the contention suite (test/contention/documents).
func TestTransfersKeepTheTotal(t *testing.T) {
	s, _ := newStore(t, Options{CommitConcurrency: 4, Transactions: TxnConfig{WaitTimeout: 5 * time.Second}})
	ctx := t.Context()
	const accounts, start = 6, 100
	var setup []Write
	for i := range accounts {
		setup = append(setup, set("bank/"+strconv.Itoa(i), doc("n", value.Int(start))))
	}
	mustCommit(t, s, setup...)
	transfer := func(rng *rand.Rand, by Principal, o TransactionOptions) error {
		from, to := rng.IntN(accounts), rng.IntN(accounts)
		if from == to {
			return nil
		}
		var retry []byte
		for attempt := 0; ; attempt++ {
			if attempt == 200 {
				return errors.New("starved")
			}
			o.Retry = retry
			id, err := s.BeginTransaction(ctx, testDB, by, o)
			if err != nil {
				return err
			}
			ref := TransactionRef{ID: id, By: by}
			retry = id
			ps := []Path{ParsePath("bank/" + strconv.Itoa(from)), ParsePath("bank/" + strconv.Itoa(to))}
			docs, _, err := s.BatchGet(ctx, testDB, ps, in(ref))
			if isCode(err, Aborted) {
				_ = s.Rollback(ctx, testDB, ref)
				continue
			}
			if err != nil {
				return err
			}
			_, err = s.CommitTransaction(ctx, testDB, ref, []Write{
				{Kind: WriteUpdate, Path: ps[0], Fields: doc("n", value.Int(intField(t, docs[0], "n")-1))},
				{Kind: WriteUpdate, Path: ps[1], Fields: doc("n", value.Int(intField(t, docs[1], "n")+1))},
			})
			if isCode(err, Aborted) {
				continue
			}
			return err
		}
	}
	var wg sync.WaitGroup
	var done atomic.Int64
	for w := range 8 {
		wg.Go(func() {
			rng := rand.New(rand.NewPCG(uint64(w), 11))
			by, o := admin, TransactionOptions{}
			if w%4 == 3 {
				by, o = endUser, TransactionOptions{Concurrency: ConcurrencyOptimistic}
			}
			for range 25 {
				if err := transfer(rng, by, o); err != nil {
					t.Error(err)
					return
				}
				if rng.IntN(5) == 0 {
					// A blind write of an unrelated document, without a transaction.
					if _, err := s.Commit(ctx, testDB, []Write{set("bank-log/"+strconv.Itoa(w), doc())}); err != nil {
						t.Error(err)
						return
					}
				}
				done.Add(1)
			}
		})
	}
	wg.Wait()
	var total int64
	for i := range accounts {
		d, _, err := s.Get(ctx, testDB, ParsePath("bank/"+strconv.Itoa(i)), ReadOptions{})
		if err != nil {
			t.Fatal(err)
		}
		total += intField(t, d, "n")
	}
	if total != accounts*start {
		t.Fatalf("total %d, want %d", total, accounts*start)
	}
	st := s.TransactionStats()
	if st.LockViolations != 0 || st.Timeouts != 0 {
		t.Fatalf("stats %+v", st)
	}
	s.SweepTransactions()
	if n, locks := s.txns.locks.table(testDB).open(); n != 0 || locks != 0 {
		t.Fatalf("%d lock transactions, %d locks left", n, locks)
	}
	t.Logf("%d transfers; %+v", done.Load(), st)
}
