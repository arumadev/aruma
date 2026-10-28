package grpc_test

import (
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/arumadev/aruma/internal/services/documents/api/apitest"
	documents "github.com/arumadev/aruma/internal/services/documents/core"
	docgrpc "github.com/arumadev/aruma/internal/services/documents/grpc"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// usageNow is the project's usage of the current hour, as the dashboard reads it.
func usageNow(t *testing.T, env *apitest.Env) documents.UsageHour {
	t.Helper()
	hs, err := env.Usage.Hours(t.Context(), env.Number, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(hs) == 0 {
		return documents.UsageHour{}
	}
	return hs[len(hs)-1]
}

// TestUsage: over gRPC, the documents written, deleted and read (Get, RunQuery, Listen) are
// counted as Firestore bills them, with the rules' decisions, the Listen stream and its targets.
func TestUsage(t *testing.T) {
	x := newHarness(t, apitest.Options{}, docgrpc.Limits{})
	x.env.PublishFirestoreRules(t, `rules_version = '2';
service cloud.firestore {
  match /databases/{database}/documents {
    match /u/{d} { allow read, write: if request.auth != null; }
  }
}`)
	tok, c := apitest.UserToken(t, "alice", nil), x.client
	x.commit(t, tok, update("u/a", intField(1)), update("u/b", intField(2)), del("u/gone"))
	if _, err := c.GetDocument(t.Context(), req(&pb.GetDocumentRequest{Name: root + "/u/a"}, tok)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetDocument(t.Context(), req(&pb.GetDocumentRequest{Name: root + "/u/a"}, "")); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("anonymous get: %v", err)
	}
	st, err := c.RunQuery(t.Context(), req(&pb.RunQueryRequest{Parent: root, QueryType: &pb.RunQueryRequest_StructuredQuery{
		StructuredQuery: &pb.StructuredQuery{From: []*pb.StructuredQuery_CollectionSelector{{CollectionId: "u"}}}}}, tok))
	if err != nil {
		t.Fatal(err)
	}
	for st.Receive() {
	}
	if err := st.Err(); err != nil {
		t.Fatal(err)
	}
	want := documents.UsageHour{Writes: 2, Deletes: 1, Reads: 3, RulesAllows: 5, RulesDenies: 1}
	if got := usageNow(t, x.env); got.Writes != want.Writes || got.Deletes != want.Deletes || got.Reads != want.Reads ||
		got.RulesAllows != want.RulesAllows || got.RulesDenies != want.RulesDenies {
		t.Errorf("after the commit, the gets and the query: %+v, want %+v", got, want)
	}

	// Listen: each document sent is a read; the stream and its targets are the peaks.
	l := apitest.OpenListen(t, c, tok)
	l.Add(queryTarget(1, &pb.StructuredQuery{From: []*pb.StructuredQuery_CollectionSelector{{CollectionId: "u"}}}))
	l.Add(docTarget(2, "u/a"))
	l.UntilGlobal()
	l.UntilGlobal()
	got := usageNow(t, x.env)
	if got.Reads != 3+3 || got.ListenersPeak != 2 || got.ConnectionsPeak != 1 {
		t.Errorf("with a Listen stream: %+v", got)
	}
	l.CloseSend()
	if err := l.Ended(); err != nil {
		t.Fatal(err)
	}
}
