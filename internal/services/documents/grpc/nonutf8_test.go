package grpc_test

import (
	"bytes"
	"encoding/binary"
	"io"
	"net/http"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/arumadev/aruma/internal/services/documents/api/apitest"
	docgrpc "github.com/arumadev/aruma/internal/services/documents/grpc"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// A string that is not UTF-8 anywhere in a gRPC request (a name, a document or collection id, a
// field name, a string value) is refused with production's generic text and no details
// (f2-non-utf8.json grpc.*). The requests are hand-encoded: a marker in a valid message becomes
// the bad bytes.
func TestNonUTF8GRPCAsProductionF2(t *testing.T) {
	x := newHarness(t, apitest.Options{}, docgrpc.Limits{})
	tok := x.env.JWT(t)
	update := func(name, field, s string) *pb.CommitRequest {
		return &pb.CommitRequest{Database: db, Writes: []*pb.Write{{Operation: &pb.Write_Update{Update: &pb.Document{
			Name: root + "/aruma-ref-f2-utf/" + name, Fields: map[string]*pb.Value{field: {ValueType: &pb.Value_StringValue{StringValue: s}}}}}}}}
	}
	for _, c := range []struct {
		id     string
		method string
		m      proto.Message
		marker string
		bad    string
	}{
		{"grpc.get_ff", "GetDocument", &pb.GetDocumentRequest{Name: root + "/aruma-ref-f2-utf/MARK~"}, "MARK~", "MARK\xff"},
		{"grpc.get_surrogate", "GetDocument", &pb.GetDocumentRequest{Name: root + "/aruma-ref-f2-utf/MARK~~~"}, "MARK~~~", "MARK\xed\xa0\x80"},
		{"grpc.commit_name_ff", "Commit", update("MARK~", "a", "x"), "MARK~", "MARK\xff"},
		{"grpc.commit_field_name_ff", "Commit", update("ok-grpc-field", "MARK~", "x"), "MARK~", "MARK\xff"},
		{"grpc.commit_string_value_ff", "Commit", update("ok-grpc-string", "a", "MARK~"), "MARK~", "MARK\xff"},
		{"grpc.create_document_id_ff", "CreateDocument", &pb.CreateDocumentRequest{Parent: root, CollectionId: "aruma-ref-f2-utf",
			DocumentId: "MARK~", Document: &pb.Document{}}, "MARK~", "MARK\xff"},
		{"grpc.list_collection_ff", "ListDocuments", &pb.ListDocumentsRequest{Parent: root, CollectionId: "MARK~"}, "MARK~", "MARK\xff"},
	} {
		b := frame(t, c.m)
		if bytes.Count(b, []byte(c.marker)) != 1 {
			t.Fatalf("%s: marker", c.id)
		}
		b = bytes.Replace(b, []byte(c.marker), []byte(c.bad), 1)
		binary.BigEndian.PutUint32(b[1:], uint32(len(b)-5))
		resp := rawCall(t, x.url, "/google.firestore.v1.Firestore/"+c.method, bytes.NewReader(b), 0, "Authorization", "Bearer "+tok)
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		st, msg, details := resp.Trailer.Get("Grpc-Status"), resp.Trailer.Get("Grpc-Message"), resp.Trailer.Get("Grpc-Status-Details-Bin")
		if resp.StatusCode != http.StatusOK || st != "3" || msg != "Request contains an invalid argument." || details != "" {
			t.Errorf("%s: HTTP %d, grpc-status %q, message %q, details %q; want 3 %q and no details", c.id, resp.StatusCode, st, msg, details, "Request contains an invalid argument.")
		}
	}
}
