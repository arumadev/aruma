package grpc_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/arumadev/aruma/internal/services/documents/api/apitest"
	docgrpc "github.com/arumadev/aruma/internal/services/documents/grpc"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb/firestorepbconnect"
)

// Transactions over gRPC (f0-errors.json grpc.txn_*, f1-get.json and f1-batch-get.json
// grpc.txn.*, grpc.new_transaction.*).

const (
	msgInvalidTransaction = "Invalid transaction."
	msgTransactionExpired = "The referenced transaction has expired or is no longer valid."
)

func begin(t *testing.T, c firestorepbconnect.FirestoreClient, tok string, o *pb.TransactionOptions) []byte {
	t.Helper()
	res, err := c.BeginTransaction(t.Context(), req(&pb.BeginTransactionRequest{Database: db, Options: o}, tok))
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if len(res.Msg.Transaction) == 0 {
		t.Fatal("BeginTransaction: no transaction")
	}
	return res.Msg.Transaction
}

func batchGet(t *testing.T, c firestorepbconnect.FirestoreClient, tok string, r *pb.BatchGetDocumentsRequest) ([]*pb.BatchGetDocumentsResponse, error) {
	t.Helper()
	s, err := c.BatchGetDocuments(t.Context(), req(r, tok))
	if err != nil {
		return nil, err
	}
	var out []*pb.BatchGetDocumentsResponse
	for s.Receive() {
		out = append(out, s.Msg())
	}
	return out, s.Err()
}

var readOnly = &pb.TransactionOptions{Mode: &pb.TransactionOptions_ReadOnly_{ReadOnly: &pb.TransactionOptions_ReadOnly{}}}

func TestTransactions(t *testing.T) {
	x := newHarness(t, apitest.Options{}, docgrpc.Limits{})
	ctx, tok := t.Context(), x.env.JWT(t)
	for _, id := range []string{"g1", "g2"} {
		if _, err := x.client.UpdateDocument(ctx, req(&pb.UpdateDocumentRequest{Document: &pb.Document{Name: root + "/t/" + id, Fields: intField(1)}}, tok)); err != nil {
			t.Fatal(err)
		}
	}

	// An id this server never issued (grpc.txn_unknown.commit), on every method that takes one.
	unknown := []byte("aruma-ref-no-such-transaction")
	_, err := x.client.Commit(ctx, req(&pb.CommitRequest{Database: db, Transaction: unknown,
		Writes: []*pb.Write{{Operation: &pb.Write_Update{Update: &pb.Document{Name: root + "/t/w", Fields: intField(1)}}}}}, tok))
	wantErr(t, "grpc.txn_unknown.commit", err, connect.CodeInvalidArgument, msgInvalidTransaction)
	_, err = x.client.GetDocument(ctx, req(&pb.GetDocumentRequest{Name: root + "/t/g1",
		ConsistencySelector: &pb.GetDocumentRequest_Transaction{Transaction: unknown}}, tok))
	wantErr(t, "get with an unknown id", err, connect.CodeInvalidArgument, msgInvalidTransaction)
	_, err = x.client.Rollback(ctx, req(&pb.RollbackRequest{Database: db, Transaction: unknown}, tok))
	wantErr(t, "rollback of an unknown id", err, connect.CodeInvalidArgument, msgInvalidTransaction)

	// A transaction committed is over (grpc.txn_reused.*).
	id := begin(t, x.client, tok, nil)
	write := func(v int64) *connect.Request[pb.CommitRequest] {
		return req(&pb.CommitRequest{Database: db, Transaction: id,
			Writes: []*pb.Write{{Operation: &pb.Write_Update{Update: &pb.Document{Name: root + "/t/reused", Fields: intField(v)}}}}}, tok)
	}
	res, err := x.client.Commit(ctx, write(1))
	if err != nil || res.Msg.CommitTime == nil || len(res.Msg.WriteResults) != 1 {
		t.Fatalf("grpc.txn_reused.commit_first: %v %v", res, err)
	}
	_, err = x.client.Commit(ctx, write(2))
	wantErr(t, "grpc.txn_reused.commit_second", err, connect.CodeAborted, msgTransactionExpired)
	// Rollback accepts a transaction that is over.
	if _, err := x.client.Rollback(ctx, req(&pb.RollbackRequest{Database: db, Transaction: id}, tok)); err != nil {
		t.Errorf("rollback after the commit: %v", err)
	}

	// Reads in a read-only transaction (grpc.txn.read_only, grpc.txn.read_only_missing).
	ro := begin(t, x.client, tok, readOnly)
	d, err := x.client.GetDocument(ctx, req(&pb.GetDocumentRequest{Name: root + "/t/g1",
		ConsistencySelector: &pb.GetDocumentRequest_Transaction{Transaction: ro}}, tok))
	if err != nil || d.Msg.Name != root+"/t/g1" {
		t.Errorf("f1-get.json grpc.txn.read_only: %v %v", d, err)
	}
	_, err = x.client.GetDocument(ctx, req(&pb.GetDocumentRequest{Name: root + "/t/nope",
		ConsistencySelector: &pb.GetDocumentRequest_Transaction{Transaction: ro}}, tok))
	wantErr(t, "f1-get.json grpc.txn.read_only_missing", err, connect.CodeNotFound, `Document "`+root+`/t/nope" not found.`)
	got, err := batchGet(t, x.client, tok, &pb.BatchGetDocumentsRequest{Database: db, Documents: []string{root + "/t/g1", root + "/t/nope"},
		ConsistencySelector: &pb.BatchGetDocumentsRequest_Transaction{Transaction: ro}})
	if err != nil || len(got) != 2 || got[0].GetFound().GetName() != root+"/t/g1" || got[1].GetMissing() != root+"/t/nope" {
		t.Errorf("f1-batch-get.json grpc.txn.read_only: %v %v", got, err)
	}
	// A write in a read-only transaction is refused, and ends it.
	_, err = x.client.Commit(ctx, req(&pb.CommitRequest{Database: db, Transaction: ro,
		Writes: []*pb.Write{{Operation: &pb.Write_Delete{Delete: root + "/t/g1"}}}}, tok))
	wantErr(t, "write in a read-only transaction", err, connect.CodeInvalidArgument, "Cannot modify entities in a read-only transaction.")

	// new_transaction: the id first, alone, then the documents (grpc.new_transaction.read_only).
	got, err = batchGet(t, x.client, tok, &pb.BatchGetDocumentsRequest{Database: db,
		Documents:           []string{root + "/t/g1", root + "/t/nope", root + "/t/g2"},
		ConsistencySelector: &pb.BatchGetDocumentsRequest_NewTransaction{NewTransaction: readOnly}})
	if err != nil || len(got) != 4 || len(got[0].Transaction) == 0 || got[0].Result != nil || got[0].ReadTime != nil ||
		got[1].GetFound().GetName() != root+"/t/g1" || got[2].GetFound().GetName() != root+"/t/g2" || got[3].GetMissing() != root+"/t/nope" {
		t.Fatalf("f1-batch-get.json grpc.new_transaction.read_only: %v %v", got, err)
	}
	for _, r := range got[1:] {
		if len(r.Transaction) != 0 {
			t.Errorf("a later response carries the transaction: %v", r)
		}
	}
	// The transaction it began is the client's: a read in it, then its rollback.
	if _, err := x.client.GetDocument(ctx, req(&pb.GetDocumentRequest{Name: root + "/t/g2",
		ConsistencySelector: &pb.GetDocumentRequest_Transaction{Transaction: got[0].Transaction}}, tok)); err != nil {
		t.Errorf("read in the new transaction: %v", err)
	}
	if _, err := x.client.Rollback(ctx, req(&pb.RollbackRequest{Database: db, Transaction: got[0].Transaction}, tok)); err != nil {
		t.Errorf("rollback of the new transaction: %v", err)
	}
	// A read that fails rolls back the transaction it began: no answer carries its id.
	got, err = batchGet(t, x.client, tok, &pb.BatchGetDocumentsRequest{Database: "projects/" + apitest.Project + "/databases/nodb",
		Documents:           []string{"projects/" + apitest.Project + "/databases/nodb/documents/t/g1"},
		ConsistencySelector: &pb.BatchGetDocumentsRequest_NewTransaction{NewTransaction: &pb.TransactionOptions{}}})
	if connect.CodeOf(err) != connect.CodeNotFound || len(got) != 0 {
		t.Errorf("new_transaction on a missing database: %v %v", got, err)
	}

	// A read-write transaction: read, write, commit; the id is the caller's alone.
	rw := begin(t, x.client, tok, &pb.TransactionOptions{Mode: &pb.TransactionOptions_ReadWrite_{ReadWrite: &pb.TransactionOptions_ReadWrite{}}})
	if _, err := x.client.GetDocument(ctx, req(&pb.GetDocumentRequest{Name: root + "/t/g1",
		ConsistencySelector: &pb.GetDocumentRequest_Transaction{Transaction: rw}}, x.env.OwnerPAT)); err == nil {
		t.Error("another principal used the transaction")
	} else {
		wantErr(t, "another principal's id", err, connect.CodeInvalidArgument, msgInvalidTransaction)
	}
	list, err := x.client.ListDocuments(ctx, req(&pb.ListDocumentsRequest{Parent: root, CollectionId: "t",
		ConsistencySelector: &pb.ListDocumentsRequest_Transaction{Transaction: rw}}, tok))
	if err != nil || len(list.Msg.Documents) < 2 {
		t.Fatalf("list in a transaction: %v %v", list, err)
	}
	res, err = x.client.Commit(ctx, req(&pb.CommitRequest{Database: db, Transaction: rw,
		Writes: []*pb.Write{{Operation: &pb.Write_Update{Update: &pb.Document{Name: root + "/t/g1", Fields: intField(2)}}}}}, tok))
	if err != nil || len(res.Msg.WriteResults) != 1 {
		t.Fatalf("commit: %v %v", res, err)
	}
	d, err = x.client.GetDocument(ctx, req(&pb.GetDocumentRequest{Name: root + "/t/g1"}, tok))
	if err != nil || d.Msg.Fields["a"].GetIntegerValue() != 2 {
		t.Errorf("after the commit: %v %v", d, err)
	}
}

// An end user (an ID token, or none) must ask for OPTIMISTIC to begin a read-write transaction
// (ADR-025 §7.2); a read-only one needs nothing. Before F5 only dev_open_rules admits one.
func TestEndUserTransactions(t *testing.T) {
	x := newHarness(t, apitest.Options{DevOpenRules: true}, docgrpc.Limits{})
	ctx := t.Context()
	_, err := x.client.BeginTransaction(ctx, req(&pb.BeginTransactionRequest{Database: db}, ""))
	wantErr(t, "end user's pessimistic transaction", err, connect.CodeInvalidArgument,
		"Third-party requests may only create OPTIMISTIC read-write transactions: set concurrency_mode to OPTIMISTIC.")
	optimistic := &pb.TransactionOptions{Mode: &pb.TransactionOptions_ReadWrite_{ReadWrite: &pb.TransactionOptions_ReadWrite{
		ConcurrencyMode: pb.TransactionOptions_OPTIMISTIC}}}
	id := begin(t, x.client, "", optimistic)
	if _, err := x.client.GetDocument(ctx, req(&pb.GetDocumentRequest{Name: root + "/o/d",
		ConsistencySelector: &pb.GetDocumentRequest_Transaction{Transaction: id}}, "")); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("read: %v", err)
	}
	// What the transaction read changed: its commit is ABORTED.
	if _, err := x.client.UpdateDocument(ctx, req(&pb.UpdateDocumentRequest{Document: &pb.Document{Name: root + "/o/d", Fields: intField(1)}}, "")); err != nil {
		t.Fatal(err)
	}
	_, err = x.client.Commit(ctx, req(&pb.CommitRequest{Database: db, Transaction: id,
		Writes: []*pb.Write{{Operation: &pb.Write_Update{Update: &pb.Document{Name: root + "/o/d", Fields: intField(2)}}}}}, ""))
	if connect.CodeOf(err) != connect.CodeAborted {
		t.Errorf("optimistic commit over a changed read: %v", err)
	}
	// The id is bound to its principal: an ID token's user may not use an anonymous caller's.
	id = begin(t, x.client, "", readOnly)
	_, err = x.client.GetDocument(ctx, req(&pb.GetDocumentRequest{Name: root + "/o/d",
		ConsistencySelector: &pb.GetDocumentRequest_Transaction{Transaction: id}}, apitest.IDToken(t)))
	wantErr(t, "an end user's id used by another", err, connect.CodeInvalidArgument, msgInvalidTransaction)
}
