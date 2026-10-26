package grpc_test

import (
	"strconv"
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
	wantErr(t, "increment by a string (f2-transforms.json grpc.inc.operand_string)", err, connect.CodeInvalidArgument, "Input must be a number.")
	_, err = commit(&pb.Write{Operation: &pb.Write_Delete{Delete: root + "/w/t"}, UpdateTransforms: []*pb.DocumentTransform_FieldTransform{serverTime("x")}})
	wantErr(t, "transforms on a delete", err, connect.CodeInvalidArgument, "a delete must not specify a update transform.")
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

// Commit applies several writes of one document in order, but for two inserts of it and a verify
// after a write of it (f2-commit.json same.*, grpc.same.update_update); BatchWrite refuses any two
// (f2-batch-write.json grpc.same_document_twice).
func TestWritesOfOneDocumentF2(t *testing.T) {
	x := newHarness(t, apitest.Options{}, docgrpc.Limits{})
	ctx, tok := t.Context(), x.env.JWT(t)
	commit := func(ws ...*pb.Write) (*pb.CommitResponse, error) {
		res, err := x.client.Commit(ctx, req(&pb.CommitRequest{Database: db, Writes: ws}, tok))
		if err != nil {
			return nil, err
		}
		return res.Msg, nil
	}
	create := func(name string) *pb.Write {
		w := update(name, intField(1))
		w.CurrentDocument = &pb.Precondition{ConditionType: &pb.Precondition_Exists{Exists: false}}
		return w
	}
	_, err := commit(create("s/s5"), create("s/s5"))
	wantErr(t, "two inserts", err, connect.CodeInvalidArgument, "Cannot insert then insert an entity in the same request.")
	verify := &pb.Write{Operation: &pb.Write_Verify{Verify: root + "/s/s6"}, CurrentDocument: &pb.Precondition{ConditionType: &pb.Precondition_Exists{Exists: true}}}
	_, err = commit(update("s/s6", intField(1)), verify)
	wantErr(t, "write then verify", err, connect.CodeInvalidArgument, "the same document cannot be written and then verified in a single request")

	res, err := commit(update("s/s1", intField(1)), update("s/s1", map[string]*pb.Value{"b": intValue(2)}))
	if err != nil || len(res.GetWriteResults()) != 2 || !proto.Equal(res.GetWriteResults()[0].GetUpdateTime(), res.GetCommitTime()) {
		t.Fatalf("update then update: %v %v", res, err)
	}
	res, err = commit(&pb.Write{Operation: &pb.Write_Delete{Delete: root + "/s/s4"}}, update("s/s4", intField(1)))
	if err != nil || !proto.Equal(res.GetWriteResults()[0].GetUpdateTime(), res.GetCommitTime()) {
		t.Fatalf("delete then update: %v %v", res, err)
	}
	res, err = commit(update("s/s3", intField(1)), &pb.Write{Operation: &pb.Write_Delete{Delete: root + "/s/s3"}})
	if err != nil || res.GetWriteResults()[0].GetUpdateTime() != nil || res.GetWriteResults()[1].GetUpdateTime() != nil {
		t.Fatalf("update then delete: %v %v", res, err)
	}

	_, err = x.client.BatchWrite(ctx, req(&pb.BatchWriteRequest{Database: db, Writes: []*pb.Write{update("s/d1", intField(1)), update("s/d1", intField(2))}}, tok))
	wantErr(t, "BatchWrite of one document twice", err, connect.CodeInvalidArgument, "the same document cannot be written more than once in a single request")

	// 501 writes in one Commit or one BatchWrite are accepted (limit.501_writes).
	many := make([]*pb.Write, 501)
	for i := range many {
		many[i] = update("many/d"+strconv.Itoa(i), intField(int64(i)))
	}
	if res, err := commit(many...); err != nil || len(res.GetWriteResults()) != 501 {
		t.Fatalf("Commit of 501 writes: %v", err)
	}
	bw, err := x.client.BatchWrite(ctx, req(&pb.BatchWriteRequest{Database: db, Writes: many}, tok))
	if err != nil || len(bw.Msg.GetStatus()) != 501 {
		t.Fatalf("BatchWrite of 501 writes: %v", err)
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
