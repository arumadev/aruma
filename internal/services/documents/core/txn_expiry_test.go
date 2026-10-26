package core

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

// An authentic id that this process does not know (issued before a restart, with the same key)
// gets ABORTED with the expired text, on which the clients retry (parity
// transaction-lost-on-restart); once it is older than production keeps a transaction (300 s,
// f2-transactions.json lifetime), "Invalid transaction.", as production's forgotten ids.
func TestTransactionUnknownAfterRestartAborted(t *testing.T) {
	ctx := t.Context()
	clk := newClock()
	key := bytes.Repeat([]byte{7}, 32)
	s1, _ := newStore(t, Options{Transactions: TxnConfig{Key: key, Now: clk.Now}})
	s2, _ := newStore(t, Options{Transactions: TxnConfig{Key: key, Now: clk.Now}})
	id, err := s1.BeginTransaction(ctx, testDB, admin, TransactionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ref := TransactionRef{ID: id, By: admin}
	if _, err := s2.CommitTransaction(ctx, testDB, ref, nil); !errors.Is(err, ErrTransactionExpired) {
		t.Fatalf("an id from before a restart: %v", err)
	}
	if err := s2.Rollback(ctx, testDB, ref); err != nil {
		t.Fatalf("rollback of an id from before a restart: %v", err)
	}
	clk.advance(301 * time.Second)
	if _, err := s2.CommitTransaction(ctx, testDB, ref, nil); !errors.Is(err, ErrInvalidTransaction) {
		t.Fatalf("an id from before a restart, 301 s old: %v", err)
	}
}
