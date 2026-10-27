package grpc_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/arumadev/aruma/internal/services/documents/api/apitest"
	docgrpc "github.com/arumadev/aruma/internal/services/documents/grpc"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// The rules on a Listen stream (api/rules_listen.go), the cases of f5-rules-listen.json under
// the rules they ran under (f5-rules-deployed.json): refused targets, ADD then REMOVE with the
// cause, the stream open; remove_target of one ends the stream; a lookup's change refuses a
// query target it allowed.

const denied = `7 "Missing or insufficient permissions."`

func deployedRules(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "test", "oracle", "documents", "corpus", "f5-rules-deployed.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Rules struct {
			Source string `json:"source"`
		} `json:"rules"`
	}
	if err := json.Unmarshal(b, &f); err != nil || f.Rules.Source == "" {
		t.Fatalf("deployed rules: %v", err)
	}
	return f.Rules.Source
}

func boolField(name string, b bool) map[string]*pb.Value {
	return map[string]*pb.Value{name: {ValueType: &pb.Value_BooleanValue{BooleanValue: b}}}
}

func pubQuery(coll string, pub *bool) *pb.StructuredQuery {
	sq := &pb.StructuredQuery{From: []*pb.StructuredQuery_CollectionSelector{{CollectionId: coll}}}
	if pub != nil {
		sq.Where = &pb.StructuredQuery_Filter{FilterType: &pb.StructuredQuery_Filter_FieldFilter{FieldFilter: &pb.StructuredQuery_FieldFilter{
			Field: &pb.StructuredQuery_FieldReference{FieldPath: "pub"}, Op: pb.StructuredQuery_FieldFilter_EQUAL,
			Value: &pb.Value{ValueType: &pb.Value_BooleanValue{BooleanValue: *pub}}}}}
	}
	return sq
}

func TestListenRules(t *testing.T) {
	x := newHarness(t, apitest.Options{}, docgrpc.Limits{})
	x.env.PublishFirestoreRules(t, deployedRules(t))
	admin, user := x.env.JWT(t), apitest.UserToken(t, "user", nil)
	x.commit(t, admin, update("aruma-ref-f5-ls/bad-1", boolField("pub", true)), update("aruma-ref-f5-ls/ok-1", boolField("pub", true)),
		update("aruma-ref-f5-ls/ok-2", boolField("pub", false)), update("aruma-ref-f5-ls2/a", intField(1)),
		update("aruma-ref-f5-ls2f/flag", boolField("open", true)))
	l := apitest.OpenListen(t, x.client, user)
	untilRemove := func(id int32) []*pb.ListenResponse {
		return l.Until(func(r *pb.ListenResponse) bool { return apitest.IsTargetChange(r, pb.TargetChange_REMOVE, id) })
	}

	// listen.doc-denied: an existing and a missing document the user cannot get, then one it can.
	l.Add(docTarget(1, "aruma-ref-f5-ls/bad-1"))
	wantShapes(t, "denied document", untilRemove(1), "ADD[1]", "REMOVE[1] "+denied)
	l.Add(docTarget(2, "aruma-ref-f5-ls/bad-missing"))
	wantShapes(t, "denied missing document", untilRemove(2), "ADD[2]", "REMOVE[2] "+denied)
	l.Add(docTarget(3, "aruma-ref-f5-ls/ok-1"))
	wantShapes(t, "allowed document", l.UntilGlobal(), "ADD[3]", "change aruma-ref-f5-ls/ok-1 [3]", "CURRENT[3]", "global")

	// listen.query-denied, listen.query-denied-other-filter, listen.query-allowed.
	l.Add(queryTarget(4, pubQuery("aruma-ref-f5-ls", nil)))
	wantShapes(t, "query without a filter", untilRemove(4), "ADD[4]", "REMOVE[4] "+denied)
	l.Add(queryTarget(5, pubQuery("aruma-ref-f5-ls", new(false))))
	wantShapes(t, "pub == false", untilRemove(5), "ADD[5]", "REMOVE[5] "+denied)
	l.Add(queryTarget(6, pubQuery("aruma-ref-f5-ls", new(true))))
	wantShapes(t, "pub == true", l.UntilGlobal(), "ADD[6]", "change aruma-ref-f5-ls/bad-1 [6]", "change aruma-ref-f5-ls/ok-1 [6]",
		"CURRENT[6]", "global")
	x.commit(t, admin, update("aruma-ref-f5-ls/ok-1", boolField("pub", false)), update("aruma-ref-f5-ls/ok-2", boolField("pub", true)))
	wantShapes(t, "a document leaves, another joins", l.UntilGlobal(), "change aruma-ref-f5-ls/ok-1 [3] -[6]",
		"change aruma-ref-f5-ls/ok-2 [6]", "global")

	// listen.query-becomes-denied: allowed by a lookup, then refused within seconds of its
	// change, without a commit of the target's own; nothing of it is sent after.
	l.Add(queryTarget(7, pubQuery("aruma-ref-f5-ls2", nil)))
	wantShapes(t, "lookup allows", l.UntilGlobal(), "ADD[7]", "change aruma-ref-f5-ls2/a [7]", "CURRENT[7]", "global")
	x.commit(t, admin, update("aruma-ref-f5-ls2f/flag", boolField("open", false)))
	wantShapes(t, "lookup refuses", untilRemove(7), "REMOVE[7] "+denied)
	x.commit(t, admin, update("aruma-ref-f5-ls2/b", intField(2)))
	l.Quiet(300 * time.Millisecond)
	// listen.query-denied-by-lookup: opened while the flag is false.
	l.Add(queryTarget(8, pubQuery("aruma-ref-f5-ls2", nil)))
	wantShapes(t, "lookup refuses at add", untilRemove(8), "ADD[8]", "REMOVE[8] "+denied)

	// listen.remove-after-denied: the refused target is not the stream's.
	l.Remove(1)
	err := l.Ended()
	if ce, ok := errors.AsType[*connect.Error](err); !ok || ce.Code() != connect.CodeNotFound || ce.Message() != "Target id not found: 1" {
		t.Errorf("remove_target of a refused target: %v", err)
	}
}

// A document target's documents are judged again before each commit is sent: one the rules no
// longer allow refuses the target, and the commit is not sent to it (ADR-026 §2).
func TestListenRulesPerDocument(t *testing.T) {
	x := newHarness(t, apitest.Options{}, docgrpc.Limits{})
	x.env.PublishFirestoreRules(t, `rules_version = '2';
service cloud.firestore {
  match /databases/{database}/documents {
    match /pd/{d} {
      allow get: if resource == null || resource.data.pub == true;
    }
  }
}`)
	admin, user := x.env.JWT(t), apitest.UserToken(t, "user", nil)
	x.commit(t, admin, update("pd/a", boolField("pub", true)))
	l := apitest.OpenListen(t, x.client, user)
	l.Add(docTarget(1, "pd/a"))
	wantShapes(t, "allowed", l.UntilGlobal(), "ADD[1]", "change pd/a [1]", "CURRENT[1]", "global")
	x.commit(t, admin, update("pd/a", boolField("pub", false)))
	wantShapes(t, "no longer allowed", l.Until(func(r *pb.ListenResponse) bool { return apitest.IsTargetChange(r, pb.TargetChange_REMOVE, 1) }),
		"REMOVE[1] "+denied)
	l.Quiet(300 * time.Millisecond)
}
