package grpc_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/arumadev/aruma/internal/services/documents/api/apitest"
	docgrpc "github.com/arumadev/aruma/internal/services/documents/grpc"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb/firestorepbconnect"
)

type writeStream = connect.BidiStreamForClient[pb.WriteRequest, pb.WriteResponse]

func openWrite(t *testing.T, x *harness, tok string) (*writeStream, *pb.WriteResponse) {
	t.Helper()
	st := x.client.Write(t.Context())
	st.RequestHeader().Set("Authorization", "Bearer "+tok)
	t.Cleanup(func() { _ = st.CloseRequest(); _ = st.CloseResponse() })
	if err := st.Send(&pb.WriteRequest{Database: db}); err != nil {
		t.Fatal(err)
	}
	res, err := st.Receive()
	if err != nil {
		t.Fatal(err)
	}
	return st, res
}

func TestWriteStream(t *testing.T) {
	x := newHarness(t, apitest.Options{}, docgrpc.Limits{})
	ctx, tok := t.Context(), x.env.JWT(t)
	st, hs := openWrite(t, x, tok)
	if hs.GetStreamId() == "" || len(hs.GetStreamToken()) == 0 || len(hs.GetWriteResults()) != 0 {
		t.Fatalf("handshake: %v", hs)
	}

	// Ten batches pipelined before any reply, each with transforms of a kind.
	arr := &pb.DocumentTransform_FieldTransform{FieldPath: "arr", TransformType: &pb.DocumentTransform_FieldTransform_AppendMissingElements{
		AppendMissingElements: arrayValue(intValue(1), intValue(2))}}
	maxT := &pb.DocumentTransform_FieldTransform{FieldPath: "hi", TransformType: &pb.DocumentTransform_FieldTransform_Maximum{Maximum: intValue(9)}}
	for i := range 10 {
		n := strconv.Itoa(i)
		req := &pb.WriteRequest{Writes: []*pb.Write{
			update("s/a"+n, intField(int64(i)), serverTime("ts"), increment("n", intValue(2)), arr, maxT),
			update("s/b"+n, intField(1)),
		}}
		if i == 0 {
			req.Writes = append(req.Writes, &pb.Write{Operation: &pb.Write_Delete{Delete: root + "/s/none"}})
		}
		if err := st.Send(req); err != nil {
			t.Fatal(err)
		}
	}
	last := hs.GetStreamToken()
	for i := range 10 {
		res, err := st.Receive()
		if err != nil {
			t.Fatalf("reply %d: %v", i, err)
		}
		wantResults := 2
		if i == 0 {
			wantResults = 3
		}
		if len(res.GetWriteResults()) != wantResults || res.GetCommitTime() == nil || res.GetStreamId() != hs.GetStreamId() ||
			len(res.GetStreamToken()) == 0 || bytes.Compare(res.GetStreamToken(), last) <= 0 {
			t.Fatalf("reply %d: %v", i, res)
		}
		last = res.GetStreamToken()
		r0 := res.GetWriteResults()[0]
		if len(r0.GetTransformResults()) != 4 || r0.GetUpdateTime() == nil || len(res.GetWriteResults()[1].GetTransformResults()) != 0 {
			t.Fatalf("reply %d results: %v", i, res)
		}
		if tv := r0.GetTransformResults()[0].GetTimestampValue(); !tv.AsTime().Equal(res.GetCommitTime().AsTime().Truncate(time.Millisecond)) ||
			r0.GetTransformResults()[1].GetIntegerValue() != 2 || r0.GetTransformResults()[3].GetIntegerValue() != 9 {
			t.Fatalf("reply %d transforms: %v", i, r0)
		}
		if i == 0 && !proto.Equal(res.GetWriteResults()[2].GetUpdateTime(), res.GetCommitTime()) {
			t.Fatalf("a delete's update_time is the commit_time: %v", res)
		}
	}
	for _, name := range []string{"s/a0", "s/b9"} {
		if _, err := x.client.GetDocument(ctx, req(&pb.GetDocumentRequest{Name: root + "/" + name}, tok)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}

	// A delete of an existing document, and an empty request, which the client sends before closing.
	if err := st.Send(&pb.WriteRequest{Writes: []*pb.Write{{Operation: &pb.Write_Delete{Delete: root + "/s/b1"}}}}); err != nil {
		t.Fatal(err)
	}
	res, err := st.Receive()
	if err != nil || len(res.GetWriteResults()) != 1 || !proto.Equal(res.GetWriteResults()[0].GetUpdateTime(), res.GetCommitTime()) {
		t.Fatalf("delete: %v %v", res, err)
	}
	if err := st.Send(&pb.WriteRequest{}); err != nil {
		t.Fatal(err)
	}
	empty, err := st.Receive()
	if err != nil || empty.GetCommitTime() == nil || len(empty.GetWriteResults()) != 0 || len(empty.GetStreamToken()) == 0 {
		t.Fatalf("empty: %v %v", empty, err)
	}

	// The legacy separate transform write.
	if err := st.Send(&pb.WriteRequest{Writes: []*pb.Write{{Operation: &pb.Write_Transform{Transform: &pb.DocumentTransform{
		Document: root + "/s/a0", FieldTransforms: []*pb.DocumentTransform_FieldTransform{increment("n", intValue(5))}}}}}}); err != nil {
		t.Fatal(err)
	}
	res, err = st.Receive()
	if err != nil || len(res.GetWriteResults()) != 1 || res.GetWriteResults()[0].GetTransformResults()[0].GetIntegerValue() != 7 {
		t.Fatalf("legacy transform: %v %v", res, err)
	}

	// The client closes its side: the stream ends cleanly.
	if err := st.CloseRequest(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Receive(); !errors.Is(err, io.EOF) {
		t.Fatalf("after close: %v", err)
	}
}

// A permanent error ends the stream with Commit's status, and the batches behind it are not applied.
func TestWriteStreamPrecondition(t *testing.T) {
	x := newHarness(t, apitest.Options{}, docgrpc.Limits{})
	ctx, tok := t.Context(), x.env.JWT(t)
	st, _ := openWrite(t, x, tok)
	stale := &pb.Precondition{ConditionType: &pb.Precondition_UpdateTime{UpdateTime: timestamppb.New(time.Unix(1, 0))}}
	bad := update("p/x", intField(1))
	bad.CurrentDocument = stale
	for _, w := range []*pb.Write{update("p/ok", intField(1)), bad, update("p/after", intField(1))} {
		if err := st.Send(&pb.WriteRequest{Writes: []*pb.Write{w}}); err != nil {
			t.Fatal(err)
		}
	}
	if res, err := st.Receive(); err != nil || len(res.GetWriteResults()) != 1 {
		t.Fatalf("first batch: %v %v", res, err)
	}
	_, err := st.Receive()
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("second batch: %v", err)
	}
	_, unary := x.client.Commit(ctx, req(&pb.CommitRequest{Database: db, Writes: []*pb.Write{bad}}, tok))
	if connect.CodeOf(unary) != connect.CodeFailedPrecondition {
		t.Fatalf("commit of the same: %v", unary)
	}
	if _, err := x.client.GetDocument(ctx, req(&pb.GetDocumentRequest{Name: root + "/p/ok"}, tok)); err != nil {
		t.Errorf("p/ok: %v", err)
	}
	for _, name := range []string{"p/x", "p/after"} {
		_, err := x.client.GetDocument(ctx, req(&pb.GetDocumentRequest{Name: root + "/" + name}, tok))
		if connect.CodeOf(err) != connect.CodeNotFound {
			t.Errorf("%s was applied: %v", name, err)
		}
	}
}

func TestWriteStreamAuth(t *testing.T) {
	x := newHarness(t, apitest.Options{}, docgrpc.Limits{})
	st := x.client.Write(t.Context())
	_ = st.Send(&pb.WriteRequest{Database: db})
	_, err := st.Receive()
	if code := connect.CodeOf(err); code != connect.CodeUnauthenticated && code != connect.CodePermissionDenied {
		t.Fatalf("no credential: %v", err)
	}
	st, _ = openWrite(t, x, x.env.JWT(t))
	_ = st.Send(&pb.WriteRequest{Writes: []*pb.Write{update("q/x", nil)}})
	if res, err := st.Receive(); err != nil || len(res.GetWriteResults()) != 1 {
		t.Fatalf("%v %v", res, err)
	}
	_ = st.Send(&pb.WriteRequest{Writes: []*pb.Write{{Operation: &pb.Write_Delete{Delete: other + "/documents/q/x"}}}})
	if _, err := st.Receive(); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("another database's name: %v", err)
	}
}

func TestWriteStreamIdle(t *testing.T) {
	x := newHarness(t, apitest.Options{}, docgrpc.Limits{StreamIdle: 300 * time.Millisecond})
	st, _ := openWrite(t, x, x.env.JWT(t))
	start := time.Now()
	_, err := st.Receive()
	if connect.CodeOf(err) != connect.CodeUnavailable || time.Since(start) > 5*time.Second {
		t.Fatalf("idle stream: %v after %v", err, time.Since(start))
	}
}

// A stream whose first message does not come is closed DEADLINE_EXCEEDED, before any work.
func TestWriteStreamFirstMessage(t *testing.T) {
	x := newHarness(t, apitest.Options{}, docgrpc.Limits{StreamFirstMessage: 300 * time.Millisecond})
	pr, pw := io.Pipe()
	t.Cleanup(func() { pw.Close() })
	r, err := http.NewRequestWithContext(t.Context(), http.MethodPost, x.url+firestorepbconnect.FirestoreWriteProcedure, pr)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/grpc")
	r.Header.Set("Authorization", "Bearer "+x.env.JWT(t))
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	resp, err := h2c(0).Do(r.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	status := resp.Trailer.Get("Grpc-Status")
	if status == "" {
		status = resp.Header.Get("Grpc-Status")
	}
	if status != "4" {
		t.Fatalf("grpc-status %q, want 4 (DEADLINE_EXCEEDED)", status)
	}
}
