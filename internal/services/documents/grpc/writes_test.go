package grpc_test

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/arumadev/aruma/internal/services/documents/api/apitest"
	docgrpc "github.com/arumadev/aruma/internal/services/documents/grpc"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

func intValue(n int64) *pb.Value {
	return &pb.Value{ValueType: &pb.Value_IntegerValue{IntegerValue: n}}
}
func doubleValue(f float64) *pb.Value {
	return &pb.Value{ValueType: &pb.Value_DoubleValue{DoubleValue: f}}
}
func arrayValue(vs ...*pb.Value) *pb.ArrayValue {
	return &pb.ArrayValue{Values: vs}
}

func update(name string, fields map[string]*pb.Value, ts ...*pb.DocumentTransform_FieldTransform) *pb.Write {
	return &pb.Write{Operation: &pb.Write_Update{Update: &pb.Document{Name: root + "/" + name, Fields: fields}}, UpdateTransforms: ts}
}

func serverTime(path string) *pb.DocumentTransform_FieldTransform {
	return &pb.DocumentTransform_FieldTransform{FieldPath: path,
		TransformType: &pb.DocumentTransform_FieldTransform_SetToServerValue{SetToServerValue: pb.DocumentTransform_FieldTransform_REQUEST_TIME}}
}

func increment(path string, v *pb.Value) *pb.DocumentTransform_FieldTransform {
	return &pb.DocumentTransform_FieldTransform{FieldPath: path, TransformType: &pb.DocumentTransform_FieldTransform_Increment{Increment: v}}
}

// Commit with every transform, in the modern form and the legacy separate transform write.
func TestCommitTransforms(t *testing.T) {
	x := newHarness(t, apitest.Options{}, docgrpc.Limits{})
	ctx, tok := t.Context(), x.env.JWT(t)
	commit := func(ws ...*pb.Write) (*pb.CommitResponse, error) {
		res, err := x.client.Commit(ctx, req(&pb.CommitRequest{Database: db, Writes: ws}, tok))
		if err != nil {
			return nil, err
		}
		return res.Msg, nil
	}
	res, err := commit(update("w/t", intField(1), serverTime("ts"), increment("n", intValue(2)),
		&pb.DocumentTransform_FieldTransform{FieldPath: "arr", TransformType: &pb.DocumentTransform_FieldTransform_AppendMissingElements{
			AppendMissingElements: arrayValue(intValue(1), doubleValue(1), intValue(2))}}))
	if err != nil {
		t.Fatal(err)
	}
	wr := res.GetWriteResults()[0]
	ts := wr.GetTransformResults()[0].GetTimestampValue()
	if len(wr.GetTransformResults()) != 3 || ts.GetNanos()%1_000_000 != 0 ||
		!ts.AsTime().Equal(res.GetCommitTime().AsTime().Truncate(time.Millisecond)) ||
		wr.GetTransformResults()[1].GetIntegerValue() != 2 || wr.GetTransformResults()[2].GetNullValue() != 0 ||
		!proto.Equal(wr.GetUpdateTime(), res.GetCommitTime()) {
		t.Fatalf("results: %v", res)
	}

	// The legacy form: a transform write after an update of the same document.
	legacy := &pb.Write{Operation: &pb.Write_Transform{Transform: &pb.DocumentTransform{Document: root + "/w/t",
		FieldTransforms: []*pb.DocumentTransform_FieldTransform{
			increment("n", doubleValue(0.5)),
			{FieldPath: "lo", TransformType: &pb.DocumentTransform_FieldTransform_Minimum{Minimum: intValue(-3)}},
			{FieldPath: "hi", TransformType: &pb.DocumentTransform_FieldTransform_Maximum{Maximum: intValue(9)}},
			{FieldPath: "arr", TransformType: &pb.DocumentTransform_FieldTransform_RemoveAllFromArray{RemoveAllFromArray: arrayValue(doubleValue(2))}},
		}}}}
	res, err = commit(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if r := res.GetWriteResults()[0].GetTransformResults(); len(r) != 4 || r[0].GetDoubleValue() != 2.5 ||
		r[1].GetIntegerValue() != -3 || r[2].GetIntegerValue() != 9 {
		t.Fatalf("legacy results: %v", res)
	}
	got, err := x.client.GetDocument(ctx, req(&pb.GetDocumentRequest{Name: root + "/w/t"}, tok))
	if err != nil {
		t.Fatal(err)
	}
	f := got.Msg.GetFields()
	if f["a"].GetIntegerValue() != 1 || f["n"].GetDoubleValue() != 2.5 || len(f["arr"].GetArrayValue().GetValues()) != 1 ||
		f["arr"].GetArrayValue().GetValues()[0].GetIntegerValue() != 1 || !proto.Equal(f["ts"].GetTimestampValue(), ts) {
		t.Fatalf("document: %v", got.Msg)
	}
	// A legacy transform of a document that does not exist creates it.
	res, err = commit(&pb.Write{Operation: &pb.Write_Transform{Transform: &pb.DocumentTransform{Document: root + "/w/new",
		FieldTransforms: []*pb.DocumentTransform_FieldTransform{serverTime("at")}}}})
	if err != nil || res.GetWriteResults()[0].GetUpdateTime() == nil {
		t.Fatalf("legacy create: %v %v", res, err)
	}

	// The writes' own errors, before the commit.
	_, err = commit(&pb.Write{Operation: &pb.Write_Verify{Verify: root + "/w/t"}})
	wantErr(t, "verify without a precondition", err, connect.CodeInvalidArgument, "a verify must contain a precondition")
	_, err = commit(update("w/t", nil, increment("n", &pb.Value{ValueType: &pb.Value_StringValue{StringValue: "1"}})))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("increment by a string: %v", err)
	}
	_, err = commit(&pb.Write{Operation: &pb.Write_Delete{Delete: root + "/w/t"}, UpdateTransforms: []*pb.DocumentTransform_FieldTransform{serverTime("x")}})
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("transforms on a delete: %v", err)
	}
	_, err = commit(update("w/t", nil, serverTime("a..b")))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("bad field path: %v", err)
	}

	// No writes: an empty answer (f1-write-results.json rest.empty).
	res, err = commit()
	if err != nil || res.GetCommitTime() != nil || len(res.GetWriteResults()) != 0 {
		t.Fatalf("empty commit: %v %v", res, err)
	}
}

// BatchWrite applies each write on its own and answers each one's status.
func TestBatchWrite(t *testing.T) {
	x := newHarness(t, apitest.Options{}, docgrpc.Limits{})
	ctx, tok := t.Context(), x.env.JWT(t)
	batch := func(ws ...*pb.Write) (*pb.BatchWriteResponse, error) {
		res, err := x.client.BatchWrite(ctx, req(&pb.BatchWriteRequest{Database: db, Writes: ws}, tok))
		if err != nil {
			return nil, err
		}
		return res.Msg, nil
	}
	exists := &pb.Precondition{ConditionType: &pb.Precondition_Exists{Exists: true}}
	missing := update("b/missing", intField(1))
	missing.CurrentDocument = exists
	res, err := batch(update("b/one", intField(1), increment("n", intValue(1))), missing, update("b/two", intField(2)))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.GetStatus()) != 3 || len(res.GetWriteResults()) != 3 {
		t.Fatalf("response: %v", res)
	}
	if res.GetStatus()[0].GetCode() != 0 || res.GetWriteResults()[0].GetUpdateTime() == nil ||
		res.GetWriteResults()[0].GetTransformResults()[0].GetIntegerValue() != 1 || res.GetStatus()[2].GetCode() != 0 {
		t.Errorf("applied writes: %v", res)
	}
	if st := res.GetStatus()[1]; st.GetCode() != int32(connect.CodeNotFound) ||
		st.GetMessage() != "No document to update: "+root+"/b/missing" || res.GetWriteResults()[1].GetUpdateTime() != nil {
		t.Errorf("failed write: %v", res)
	}
	for _, name := range []string{"b/one", "b/two"} {
		if _, err := x.client.GetDocument(ctx, req(&pb.GetDocumentRequest{Name: root + "/" + name}, tok)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	_, err = batch(update("b/one", intField(1)), &pb.Write{Operation: &pb.Write_Delete{Delete: root + "/b/one"}})
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("two writes to one document: %v", err)
	}
	_, err = x.client.BatchWrite(ctx, req(&pb.BatchWriteRequest{Database: db, Writes: []*pb.Write{
		{Operation: &pb.Write_Update{Update: &pb.Document{Name: other + "/documents/b/x"}}}}}, tok))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("another project's document: %v", err)
	}
}
