// Package documents holds the Document Database's contention tests: what a pessimistic
// transaction's locks make other writers wait for, against production's recordings
// (test/oracle/documents/corpus/f2-locks.json).
package documents

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/arumadev/aruma/internal/services/documents/core"
	"github.com/arumadev/aruma/internal/services/documents/value"
	"github.com/arumadev/aruma/internal/store/storetest"
)

var (
	admin  = core.Principal{Admin: true, ID: "contention@aruma.iam.gserviceaccount.com"}
	testDB = core.DB{Project: 731_000_000_002, ProjectID: "contention", ID: core.DefaultDatabase}
)

// blockedAfter is how long a write may take before it counts as waiting for the transaction.
const blockedAfter = 300 * time.Millisecond

// TestPhantom: a pessimistic transaction's listing of a collection locks the collection under
// its parent (parity coarse-query-locks). A phantom insert into the listed collection, and any
// other write to it, waits until the transaction ends, as production's phantom insert does into
// a queried range (f2-locks.json range.insert_in_range, whole.insert); a write to the same
// collection id under another parent, or to another collection, does not wait, as production's
// does not (range.sibling_parent, range.other_collection, whole.sibling_parent).
//
// Production locks only the range a query scans, so an insert below the range or without the
// queried field, and an update of a document the query did not match, do not wait there
// (range.insert_below_range, range.insert_no_field, range.update_unscanned): they wait here.
// That is the exception, and the cases below that wait on Aruma alone say so. Queries are F3's;
// a listing takes the lock a query of the whole collection does.
func TestPhantom(t *testing.T) {
	store := core.NewPGStore(storetest.NewPool(t), core.Options{Transactions: core.TxnConfig{WaitTimeout: 10 * time.Second}})
	ctx := t.Context()
	setup := make([]core.Write, 0, 6)
	for i := range 6 {
		setup = append(setup, set("lock/p1/items/i"+strconv.Itoa(i+1), int64(i+1)))
	}
	if _, err := store.Commit(ctx, testDB, setup); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name  string
		write core.Write
		waits bool
		// production is what production did with a query of n > 3 in place of the listing,
		// when it differs.
		production string
	}{
		{name: "phantom insert (range.insert_in_range, whole.insert)", write: set("lock/p1/items/new-in", 5), waits: true},
		{name: "update of a listed document (range.update_scanned)", write: set("lock/p1/items/i5", 50), waits: true},
		{name: "insert below the range (range.insert_below_range)", write: set("lock/p1/items/new-out", 1), waits: true,
			production: "does not wait: the insert is outside the range n > 3"},
		{name: "update of an unmatched document (range.update_unscanned)", write: set("lock/p1/items/i1", 10), waits: true,
			production: "does not wait: i1 has n 1, outside the range"},
		{name: "insert without the field (range.insert_no_field)", write: core.Write{Kind: core.WriteUpdate, Path: core.ParsePath("lock/p1/items/new-nofield"), Fields: value.Map()}, waits: true,
			production: "does not wait: a document without n is not in the range"},
		{name: "same collection id, other parent (range.sibling_parent, whole.sibling_parent)", write: set("lock/p2/items/new-sib", 5)},
		{name: "other collection (range.other_collection)", write: set("lock/p1/others/new-oth", 5)},
	} {
		t.Run(c.name, func(t *testing.T) {
			id, err := store.BeginTransaction(ctx, testDB, admin, core.TransactionOptions{})
			if err != nil {
				t.Fatal(err)
			}
			ref := core.TransactionRef{ID: id, By: admin}
			res, err := store.ListDocuments(ctx, testDB, core.ListRequest{Parent: core.ParsePath("lock/p1"), CollectionID: "items",
				ReadOptions: core.ReadOptions{Transaction: &ref}})
			if err != nil || len(res.Documents) == 0 {
				t.Fatalf("listing: %d documents, %v", len(res.Documents), err)
			}
			done := make(chan error, 1)
			go func() {
				_, err := store.Commit(context.WithoutCancel(ctx), testDB, []core.Write{c.write})
				done <- err
			}()
			waited := false
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("the write: %v", err)
				}
			case <-time.After(blockedAfter):
				waited = true
			}
			if err := store.Rollback(ctx, testDB, ref); err != nil {
				t.Fatal(err)
			}
			if waited {
				if err := <-done; err != nil {
					t.Fatalf("the write, once the transaction ended: %v", err)
				}
			}
			if waited != c.waits {
				t.Fatalf("waited %v, want %v", waited, c.waits)
			}
			if c.production != "" {
				t.Logf("waits on Aruma; production %s (parity coarse-query-locks)", c.production)
			}
			if txns, locks := store.OpenLocks(testDB); txns != 0 || locks != 0 {
				t.Fatalf("%d transactions, %d locks left", txns, locks)
			}
		})
	}
}

func set(path string, n int64) core.Write {
	return core.Write{Kind: core.WriteUpdate, Path: core.ParsePath(path), Fields: value.Map(value.Field{Name: "n", Value: value.Int(n)})}
}
