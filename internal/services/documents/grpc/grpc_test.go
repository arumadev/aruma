package grpc_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/arumadev/aruma/internal/services/documents/api/apitest"
	documents "github.com/arumadev/aruma/internal/services/documents/core"
	docgrpc "github.com/arumadev/aruma/internal/services/documents/grpc"
	"github.com/arumadev/aruma/internal/services/documents/value"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/adminpb"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/adminpb/adminpbconnect"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb/firestorepbconnect"
)

// The gRPC surface, in process: the real connect client over h2c against the route on a test
// server, the service layer and core on Postgres. The texts are production's, as recorded in
// test/oracle/documents/corpus/f1-*.json; the case each check reproduces is named beside it.

const (
	db    = "projects/" + apitest.Project + "/databases/(default)"
	root  = db + "/documents"
	other = "projects/" + apitest.OtherProject + "/databases/(default)"
)

type harness struct {
	env    *apitest.Env
	h      *docgrpc.Handler
	url    string
	client firestorepbconnect.FirestoreClient
}

func newHarness(t *testing.T, o apitest.Options, limits docgrpc.Limits) *harness {
	t.Helper()
	env := apitest.New(t, o)
	h := docgrpc.New(docgrpc.Options{Service: env.Service, Limits: limits, Metrics: docgrpc.NewMetrics(),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	ts := httptest.NewUnstartedServer(h)
	var p http.Protocols
	p.SetHTTP1(true)
	p.SetUnencryptedHTTP2(true)
	ts.Config.Protocols = &p
	// Aruma's own timeouts (internal/server.Run), which the route shortens and lifts.
	ts.Config.ReadTimeout, ts.Config.WriteTimeout = 60*time.Second, 60*time.Second
	ts.Start()
	t.Cleanup(ts.Close)
	return &harness{env: env, h: h, url: ts.URL,
		client: firestorepbconnect.NewFirestoreClient(h2c(0), ts.URL, connect.WithGRPC())}
}

// h2c is an HTTP/2 client without TLS, as every gRPC client speaks to a development server.
// window, when set, is the client's receive window per stream.
func h2c(window int) *http.Client {
	var p http.Protocols
	p.SetUnencryptedHTTP2(true)
	tr := &http.Transport{Protocols: &p}
	if window > 0 {
		tr.HTTP2 = &http.HTTP2Config{MaxReceiveBufferPerStream: window, MaxReceiveBufferPerConnection: window}
	}
	return &http.Client{Transport: tr}
}

func req[T any](m *T, token string) *connect.Request[T] {
	r := connect.NewRequest(m)
	if token != "" {
		r.Header().Set("Authorization", "Bearer "+token)
	}
	return r
}

func wantErr(t *testing.T, what string, err error, code connect.Code, msg string) {
	t.Helper()
	if err == nil {
		t.Errorf("%s: no error, want %v %q", what, code, msg)
		return
	}
	ce, ok := errors.AsType[*connect.Error](err)
	if !ok {
		t.Errorf("%s: %v is not a gRPC status", what, err)
		return
	}
	if ce.Code() != code || ce.Message() != msg {
		t.Errorf("%s: %v %q, want %v %q", what, ce.Code(), ce.Message(), code, msg)
	}
}

func intField(n int64) map[string]*pb.Value {
	return map[string]*pb.Value{"a": {ValueType: &pb.Value_IntegerValue{IntegerValue: n}}}
}

func TestF1Methods(t *testing.T) {
	x := newHarness(t, apitest.Options{}, docgrpc.Limits{})
	ctx, tok, c := t.Context(), x.env.JWT(t), x.client

	// CreateDocument (f1-create.json).
	created, err := c.CreateDocument(ctx, req(&pb.CreateDocumentRequest{Parent: root, CollectionId: "w", DocumentId: "c1",
		Document: &pb.Document{Fields: intField(1)}}, tok))
	if err != nil {
		t.Fatal(err)
	}
	if d := created.Msg; d.Name != root+"/w/c1" || d.Fields["a"].GetIntegerValue() != 1 || !d.CreateTime.AsTime().Equal(d.UpdateTime.AsTime()) {
		t.Errorf("created %v", d)
	}
	_, err = c.CreateDocument(ctx, req(&pb.CreateDocumentRequest{Parent: root, CollectionId: "w", DocumentId: "c1",
		Document: &pb.Document{Fields: intField(1)}}, tok))
	wantErr(t, "grpc.existing_id", err, connect.CodeAlreadyExists, "Document already exists: "+root+"/w/c1")
	auto, err := c.CreateDocument(ctx, req(&pb.CreateDocumentRequest{Parent: root, CollectionId: "w", Document: &pb.Document{}}, tok))
	if err != nil {
		t.Fatal(err)
	}
	if id := strings.TrimPrefix(auto.Msg.Name, root+"/w/"); len(id) != 20 {
		t.Errorf("auto id %q", auto.Msg.Name)
	}
	_, err = c.CreateDocument(ctx, req(&pb.CreateDocumentRequest{Parent: root, CollectionId: "w", DocumentId: "__x__"}, tok))
	wantErr(t, "rest.reserved_id", err, connect.CodeInvalidArgument, `Resource id "__x__" is invalid because it is reserved.`)
	_, err = c.CreateDocument(ctx, req(&pb.CreateDocumentRequest{Parent: root, CollectionId: "w", DocumentId: "c4",
		Document: &pb.Document{Name: root + "/w/other"}}, tok))
	wantErr(t, "rest.name_in_body", err, connect.CodeInvalidArgument, "document.name must not be set")

	// GetDocument (f1-get.json).
	got, err := c.GetDocument(ctx, req(&pb.GetDocumentRequest{Name: root + "/w/c1"}, tok))
	if err != nil || !proto.Equal(got.Msg, created.Msg) {
		t.Errorf("get: %v %v, want %v", got, err, created.Msg)
	}
	masked, err := c.GetDocument(ctx, req(&pb.GetDocumentRequest{Name: root + "/w/c1", Mask: &pb.DocumentMask{}}, tok))
	if err != nil || masked.Msg.Fields != nil || masked.Msg.UpdateTime == nil {
		t.Errorf("grpc.mask_empty_list: %v %v", masked, err)
	}
	_, err = c.GetDocument(ctx, req(&pb.GetDocumentRequest{Name: root + "/w/nope"}, tok))
	wantErr(t, "grpc.missing", err, connect.CodeNotFound, `Document "`+root+`/w/nope" not found.`)
	_, err = c.GetDocument(ctx, req(&pb.GetDocumentRequest{Name: root + "/w/c1",
		ConsistencySelector: &pb.GetDocumentRequest_ReadTime{ReadTime: timestamppb.New(time.Now().Add(2 * time.Hour).Truncate(time.Minute))}}, tok))
	wantErr(t, "grpc.read_time.future", err, connect.CodeInvalidArgument, "The requested 'read_time' cannot be in the future.")
	_, err = c.GetDocument(ctx, req(&pb.GetDocumentRequest{Name: root + "/w/c1",
		ConsistencySelector: &pb.GetDocumentRequest_ReadTime{ReadTime: timestamppb.New(time.Now().Add(-2 * time.Hour).Truncate(time.Minute))}}, tok))
	wantErr(t, "grpc.read_time.2h_whole_minute", err, connect.CodeFailedPrecondition, "The requested 'read_time' is too old.")
	_, err = c.GetDocument(ctx, req(&pb.GetDocumentRequest{Name: root + "/w/c1",
		ConsistencySelector: &pb.GetDocumentRequest_ReadTime{ReadTime: &timestamppb.Timestamp{Seconds: time.Now().Unix() - 10, Nanos: 789}}}, tok))
	wantErr(t, "rest.read_time.nanoseconds", err, connect.CodeInvalidArgument, "timestamp cannot have more than microseconds precision")
	atCreate, err := c.GetDocument(ctx, req(&pb.GetDocumentRequest{Name: root + "/w/c1",
		ConsistencySelector: &pb.GetDocumentRequest_ReadTime{ReadTime: created.Msg.UpdateTime}}, tok))
	if err != nil || !proto.Equal(atCreate.Msg, created.Msg) {
		t.Errorf("grpc.read_time.setup_commit: %v %v", atCreate, err)
	}

	// UpdateDocument (f1-update.json).
	upd, err := c.UpdateDocument(ctx, req(&pb.UpdateDocumentRequest{Document: &pb.Document{Name: root + "/w/u1",
		Fields: map[string]*pb.Value{"g": {ValueType: &pb.Value_IntegerValue{IntegerValue: 1}}}}}, tok))
	if err != nil || upd.Msg.Fields["g"].GetIntegerValue() != 1 {
		t.Fatalf("grpc.no_mask: %v %v", upd, err)
	}
	same, err := c.UpdateDocument(ctx, req(&pb.UpdateDocumentRequest{Document: &pb.Document{Name: root + "/w/u1",
		Fields: intField(9)}, UpdateMask: &pb.DocumentMask{}}, tok))
	if err != nil || len(same.Msg.Fields) != 1 || same.Msg.Fields["g"] == nil {
		t.Errorf("grpc.mask_empty_list: %v %v", same, err)
	}
	one, err := c.UpdateDocument(ctx, req(&pb.UpdateDocumentRequest{Document: &pb.Document{Name: root + "/w/u1",
		Fields: intField(30)}, UpdateMask: &pb.DocumentMask{FieldPaths: []string{"a"}}}, tok))
	if err != nil || one.Msg.Fields["a"].GetIntegerValue() != 30 || one.Msg.Fields["g"] == nil ||
		!one.Msg.CreateTime.AsTime().Equal(upd.Msg.CreateTime.AsTime()) {
		t.Errorf("grpc.mask_one: %v %v", one, err)
	}
	_, err = c.UpdateDocument(ctx, req(&pb.UpdateDocumentRequest{Document: &pb.Document{Name: root + "/w/u1", Fields: intField(31)},
		CurrentDocument: &pb.Precondition{ConditionType: &pb.Precondition_Exists{Exists: false}}}, tok))
	wantErr(t, "grpc.exists_false_on_existing", err, connect.CodeAlreadyExists, "Document already exists: "+root+"/w/u1")
	_, err = c.UpdateDocument(ctx, req(&pb.UpdateDocumentRequest{Document: &pb.Document{Name: root + "/w/u-missing", Fields: intField(1)},
		CurrentDocument: &pb.Precondition{ConditionType: &pb.Precondition_Exists{Exists: true}}}, tok))
	wantErr(t, "grpc.exists_true_on_missing", err, connect.CodeNotFound, "No document to update: "+root+"/w/u-missing")
	stale := time.Date(2001, 1, 1, 0, 0, 0, 1000, time.UTC)
	_, err = c.UpdateDocument(ctx, req(&pb.UpdateDocumentRequest{Document: &pb.Document{Name: root + "/w/u1", Fields: intField(32)},
		CurrentDocument: &pb.Precondition{ConditionType: &pb.Precondition_UpdateTime{UpdateTime: timestamppb.New(stale)}}}, tok))
	wantErr(t, "grpc.update_time_stale", err, connect.CodeFailedPrecondition,
		"the stored version ("+strconv.FormatInt(one.Msg.UpdateTime.AsTime().UnixMicro(), 10)+") does not match the required base version (978307200000001)")

	// BatchGetDocuments (f1-batch-get.json grpc.order, grpc.duplicates): each document once, the
	// found ones in name order, then the missing ones.
	stream, err := c.BatchGetDocuments(ctx, req(&pb.BatchGetDocumentsRequest{Database: db,
		Documents: []string{root + "/w/u1", root + "/w/nope", root + "/w/c1", root + "/w/c1", root + "/w/nope", root + "/w/nope2"},
		Mask:      &pb.DocumentMask{FieldPaths: []string{"a"}}}, tok))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for stream.Receive() {
		r := stream.Msg()
		if r.ReadTime == nil {
			t.Error("a response without read_time")
		}
		if f := r.GetFound(); f != nil {
			names = append(names, "found "+f.Name)
			if f.Fields["g"] != nil {
				t.Error("the mask let g through")
			}
		} else {
			names = append(names, "missing "+r.GetMissing())
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	if want := []string{"found " + root + "/w/c1", "found " + root + "/w/u1", "missing " + root + "/w/nope2", "missing " + root + "/w/nope"}; !slices.Equal(names, want) {
		t.Errorf("batch get: %q, want %q", names, want)
	}
	empty, err := c.BatchGetDocuments(ctx, req(&pb.BatchGetDocumentsRequest{Database: db}, tok))
	if err != nil {
		t.Fatal(err)
	}
	for empty.Receive() {
		t.Error("grpc.empty_list: a response")
	}
	if err := empty.Err(); err != nil {
		t.Error(err)
	}

	// ListDocuments (f1-list-documents.json).
	for _, id := range []string{"d1", "d2", "d3", "d4", "d5"} {
		if _, err := c.UpdateDocument(ctx, req(&pb.UpdateDocumentRequest{Document: &pb.Document{Name: root + "/l/" + id, Fields: intField(1)}}, tok)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.UpdateDocument(ctx, req(&pb.UpdateDocumentRequest{Document: &pb.Document{Name: root + "/l/m/sub/z", Fields: intField(3)}}, tok)); err != nil {
		t.Fatal(err)
	}
	var listed []string
	token := ""
	for page := 0; ; page++ {
		res, err := c.ListDocuments(ctx, req(&pb.ListDocumentsRequest{Parent: root, CollectionId: "l", PageSize: 2, PageToken: token}, tok))
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range res.Msg.Documents {
			listed = append(listed, strings.TrimPrefix(d.Name, root+"/l/"))
		}
		if token = res.Msg.NextPageToken; token == "" || page > 5 {
			break
		}
	}
	if want := []string{"d1", "d2", "d3", "d4", "d5"}; !slices.Equal(listed, want) {
		t.Errorf("grpc.paging: %q, want %q", listed, want)
	}
	// Ordered by a field (rest.paging_order_by_n_desc): ties broken by name in the order's
	// direction, the documents without the field left out, pages resumed by value and name.
	if _, err := c.UpdateDocument(ctx, req(&pb.UpdateDocumentRequest{Document: &pb.Document{Name: root + "/l/d3", Fields: intField(0)}}, tok)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.UpdateDocument(ctx, req(&pb.UpdateDocumentRequest{Document: &pb.Document{Name: root + "/l/d6",
		Fields: map[string]*pb.Value{"s": {ValueType: &pb.Value_StringValue{StringValue: "no a"}}}}}, tok)); err != nil {
		t.Fatal(err)
	}
	listed, token = nil, ""
	for page := 0; ; page++ {
		res, err := c.ListDocuments(ctx, req(&pb.ListDocumentsRequest{Parent: root, CollectionId: "l", OrderBy: "a desc", PageSize: 2, PageToken: token}, tok))
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range res.Msg.Documents {
			listed = append(listed, strings.TrimPrefix(d.Name, root+"/l/"))
		}
		if token = res.Msg.NextPageToken; token == "" || page > 5 {
			break
		}
	}
	if want := []string{"d5", "d4", "d2", "d1", "d3"}; !slices.Equal(listed, want) {
		t.Errorf("ordered paging: %q, want %q", listed, want)
	}
	_, err = c.ListDocuments(ctx, req(&pb.ListDocumentsRequest{Parent: root, CollectionId: "l", OrderBy: "a", PageSize: 2, PageToken: firstPageToken(t, c, tok)}, tok))
	wantErr(t, "rest.token.other_order_by", err, connect.CodeInvalidArgument, "Invalid page token.")
	missing, err := c.ListDocuments(ctx, req(&pb.ListDocumentsRequest{Parent: root, CollectionId: "l", ShowMissing: true}, tok))
	if err != nil || len(missing.Msg.Documents) != 7 || missing.Msg.Documents[6].Name != root+"/l/m" || missing.Msg.Documents[6].UpdateTime != nil {
		t.Errorf("rest.show_missing: %v %v", missing, err)
	}
	sub, err := c.ListDocuments(ctx, req(&pb.ListDocumentsRequest{Parent: root + "/l/m"}, tok))
	if err != nil || len(sub.Msg.Documents) != 1 || sub.Msg.Documents[0].Name != root+"/l/m/sub/z" {
		t.Errorf("grpc.empty_collection_id_missing_parent: %v %v", sub, err)
	}
	for _, c2 := range []struct {
		id   string
		req  *pb.ListDocumentsRequest
		code connect.Code
		msg  string
	}{
		{"grpc.order_by_bad", &pb.ListDocumentsRequest{Parent: root, CollectionId: "l", OrderBy: "foo bar"}, connect.CodeInvalidArgument, `Invalid order by clause "foo bar".`},
		{"grpc.token_garbage", &pb.ListDocumentsRequest{Parent: root, CollectionId: "l", PageSize: 2, PageToken: "abc"}, connect.CodeInvalidArgument, "invalid page token"},
		{"grpc.page_size_negative", &pb.ListDocumentsRequest{Parent: root, CollectionId: "l", PageSize: -1}, connect.CodeInvalidArgument, "Page size must be nonnegative."},
		{"grpc.empty_collection_id_show_missing", &pb.ListDocumentsRequest{Parent: root + "/l/d1", ShowMissing: true}, connect.CodeInvalidArgument, "collection id must be set when show_missing is true"},
		{"grpc.empty_collection_id_order_by_name_desc", &pb.ListDocumentsRequest{Parent: root + "/l/d1", OrderBy: "__name__ desc"}, connect.CodeInvalidArgument, "kind is required for all orders except __key__ ascending"},
		{"rest.order_by.18", &pb.ListDocumentsRequest{Parent: root, CollectionId: "l", OrderBy: "__name__,n"}, connect.CodeInvalidArgument, "order by clause cannot contain more fields after the key"},
		{"rest.order_by.30", &pb.ListDocumentsRequest{Parent: root, CollectionId: "l", OrderBy: "n,n"}, connect.CodeInvalidArgument, "order by clause cannot contain duplicate fields n"},
		{"rest.show_missing_order_by_name_desc", &pb.ListDocumentsRequest{Parent: root, CollectionId: "l", ShowMissing: true, OrderBy: "__name__ desc"}, connect.CodeInvalidArgument, "cannot specify an order when show_missing is true"},
		{"grpc.read_time_2h", &pb.ListDocumentsRequest{Parent: root, CollectionId: "l",
			ConsistencySelector: &pb.ListDocumentsRequest_ReadTime{ReadTime: timestamppb.New(time.Now().Add(-2 * time.Hour).Truncate(time.Minute))}},
			connect.CodeFailedPrecondition, "The requested 'read_time' is too old."},
	} {
		_, err := c.ListDocuments(ctx, req(c2.req, tok))
		wantErr(t, c2.id, err, c2.code, c2.msg)
	}
	firstPage, err := c.ListDocuments(ctx, req(&pb.ListDocumentsRequest{Parent: root, CollectionId: "l", PageSize: 2}, tok))
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.ListDocuments(ctx, req(&pb.ListDocumentsRequest{Parent: root, CollectionId: "never", PageSize: 2, PageToken: firstPage.Msg.NextPageToken}, tok))
	wantErr(t, "rest.token.other_collection", err, connect.CodeInvalidArgument, "Invalid page token.")
	other, err := c.ListDocuments(ctx, req(&pb.ListDocumentsRequest{Parent: root, CollectionId: "l", PageSize: 3, PageToken: firstPage.Msg.NextPageToken}, tok))
	if err != nil || len(other.Msg.Documents) != 3 || other.Msg.Documents[0].Name != root+"/l/d3" {
		t.Errorf("rest.token.other_page_size: %v %v", other, err)
	}

	// ListCollectionIds (f1-list-collection-ids.json).
	for _, id := range []string{"c1", "c2", "c3"} {
		if _, err := c.UpdateDocument(ctx, req(&pb.UpdateDocumentRequest{Document: &pb.Document{Name: root + "/p/x/" + id + "/k"}}, tok)); err != nil {
			t.Fatal(err)
		}
	}
	ids1, err := c.ListCollectionIds(ctx, req(&pb.ListCollectionIdsRequest{Parent: root + "/p/x", PageSize: 2}, tok))
	if err != nil || !slices.Equal(ids1.Msg.CollectionIds, []string{"c1", "c2"}) || ids1.Msg.NextPageToken == "" {
		t.Fatalf("grpc.paging.page1: %v %v", ids1, err)
	}
	ids2, err := c.ListCollectionIds(ctx, req(&pb.ListCollectionIdsRequest{Parent: root + "/p/x", PageSize: 2, PageToken: ids1.Msg.NextPageToken}, tok))
	if err != nil || !slices.Equal(ids2.Msg.CollectionIds, []string{"c3"}) || ids2.Msg.NextPageToken != "" {
		t.Errorf("grpc.paging.page2: %v %v", ids2, err)
	}
	_, err = c.ListCollectionIds(ctx, req(&pb.ListCollectionIdsRequest{Parent: root + "/p/x", PageToken: "abc"}, tok))
	wantErr(t, "grpc.token_garbage", err, connect.CodeInvalidArgument, "invalid page token")
	never, err := c.ListCollectionIds(ctx, req(&pb.ListCollectionIdsRequest{Parent: root + "/never/x"}, tok))
	if err != nil || len(never.Msg.CollectionIds) != 0 {
		t.Errorf("rest.never_written_parent: %v %v", never, err)
	}

	// DeleteDocument (f1-delete.json).
	if _, err := c.DeleteDocument(ctx, req(&pb.DeleteDocumentRequest{Name: root + "/w/d-missing"}, tok)); err != nil {
		t.Errorf("grpc.missing: %v", err)
	}
	_, err = c.DeleteDocument(ctx, req(&pb.DeleteDocumentRequest{Name: root + "/w/d-missing",
		CurrentDocument: &pb.Precondition{ConditionType: &pb.Precondition_Exists{Exists: true}}}, tok))
	wantErr(t, "grpc.missing_exists_true", err, connect.CodeNotFound, "No document to update: "+root+"/w/d-missing")
	_, err = c.DeleteDocument(ctx, req(&pb.DeleteDocumentRequest{Name: root + "/w/c1",
		CurrentDocument: &pb.Precondition{ConditionType: &pb.Precondition_Exists{Exists: false}}}, tok))
	wantErr(t, "grpc.exists_false_on_existing", err, connect.CodeAlreadyExists, "Document already exists: "+root+"/w/c1")
	if _, err := c.DeleteDocument(ctx, req(&pb.DeleteDocumentRequest{Name: root + "/w/c1"}, tok)); err != nil {
		t.Fatal(err)
	}
	_, err = c.GetDocument(ctx, req(&pb.GetDocumentRequest{Name: root + "/w/c1"}, tok))
	wantErr(t, "deleted", err, connect.CodeNotFound, `Document "`+root+`/w/c1" not found.`)
}

// Every name of a request binds to one database (f1-resource-names.json), and a database that
// does not exist is NOT_FOUND with no message on gRPC (f0-missing-database.json).
func TestResourceNames(t *testing.T) {
	x := newHarness(t, apitest.Options{}, docgrpc.Limits{})
	ctx, tok, c := t.Context(), x.env.JWT(t), x.client
	stream, err := c.BatchGetDocuments(ctx, req(&pb.BatchGetDocumentsRequest{Database: db,
		Documents: []string{root + "/a/b", other + "/documents/a/b"}}, tok))
	if err == nil {
		for stream.Receive() {
			t.Error("a response to a request naming two projects")
		}
		err = stream.Err()
	}
	wantErr(t, "rest.batch_get.other_project", err, connect.CodeInvalidArgument,
		"The request was for database '"+db+"' but was attempting to access database '"+other+"'")
	_, err = c.GetDocument(ctx, req(&pb.GetDocumentRequest{Name: other + "/documents/a/b"}, tok))
	wantErr(t, "another project's service account", err, connect.CodePermissionDenied, "Missing or insufficient permissions.")
	_, err = c.GetDocument(ctx, req(&pb.GetDocumentRequest{Name: "projects/no-such-project/databases/(default)/documents/a/b"}, tok))
	wantErr(t, "a project that does not exist", err, connect.CodePermissionDenied, "Missing or insufficient permissions.")
	_, err = c.GetDocument(ctx, req(&pb.GetDocumentRequest{Name: "projects/" + apitest.Project + "/databases/aruma-ref-nonexistent/documents/a/b"}, tok))
	wantErr(t, "f0-missing-database grpc", err, connect.CodeNotFound, "")
	_, err = c.GetDocument(ctx, req(&pb.GetDocumentRequest{Name: "a/b"}, tok))
	wantErr(t, "rest.relative_name", err, connect.CodeInvalidArgument, `Document name "a/b" lacks "projects" at index 0.`)
	// The resource prefix of another project is ignored where the body names the database
	// (grpc.get.prefix_other_project).
	r := req(&pb.GetDocumentRequest{Name: root + "/a/missing"}, tok)
	r.Header().Set("Google-Cloud-Resource-Prefix", other)
	_, err = c.GetDocument(ctx, r)
	wantErr(t, "grpc.get.prefix_other_project", err, connect.CodeNotFound, `Document "`+root+`/a/missing" not found.`)
}

// The callers before F5 (ADR-029 §2, ADR-025 §8): admins only.
func TestCallers(t *testing.T) {
	x := newHarness(t, apitest.Options{DevOwnerToken: true}, docgrpc.Limits{})
	ctx, c := t.Context(), x.client
	get := func(token, name string) error {
		_, err := c.GetDocument(ctx, req(&pb.GetDocumentRequest{Name: name}, token))
		return err
	}
	missing := `Document "` + root + `/a/b" not found.`
	wantErr(t, "service account JWT", get(x.env.JWT(t), root+"/a/b"), connect.CodeNotFound, missing)
	wantErr(t, "dev owner on loopback", get("owner", root+"/a/b"), connect.CodeNotFound, missing)
	wantErr(t, "dev owner, another project", get("owner", other+"/documents/a/b"), connect.CodeNotFound, `Document "`+other+`/documents/a/b" not found.`)
	wantErr(t, "owner's aruma_pat_", get(x.env.OwnerPAT, other+"/documents/a/b"), connect.CodeNotFound, `Document "`+other+`/documents/a/b" not found.`)
	wantErr(t, "end user before F5", get(apitest.IDToken(t), root+"/a/b"), connect.CodePermissionDenied, "Missing or insufficient permissions.")
	wantErr(t, "anonymous before F5", get("", root+"/a/b"), connect.CodePermissionDenied, "Missing or insufficient permissions.")
	if err := get("not-a-token", root+"/a/b"); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("garbage credential: %v", err)
	}

	// Without the flag, owner is refused with ADR-029 §6's text.
	y := newHarness(t, apitest.Options{}, docgrpc.Limits{})
	_, err := y.client.GetDocument(ctx, req(&pb.GetDocumentRequest{Name: root + "/a/b"}, "owner"))
	wantErr(t, "owner without dev_owner_token", err, connect.CodeUnauthenticated,
		"Bearer owner is accepted only when documents.dev_owner_token is set on a server whose public_url and listen are loopback addresses; connect with TLS to send the service account's credentials.")

	// documents.dev_open_rules opens the database to this machine.
	z := newHarness(t, apitest.Options{DevOpenRules: true}, docgrpc.Limits{})
	_, err = z.client.GetDocument(ctx, req(&pb.GetDocumentRequest{Name: root + "/a/b"}, ""))
	wantErr(t, "anonymous with dev_open_rules", err, connect.CodeNotFound, missing)
}

// Every method not served yet, and every unknown one, answers UNIMPLEMENTED in gRPC form.
func TestUnimplemented(t *testing.T) {
	x := newHarness(t, apitest.Options{}, docgrpc.Limits{})
	ctx, tok := t.Context(), x.env.JWT(t)
	st, err := x.client.ExecutePipeline(ctx, req(&pb.ExecutePipelineRequest{Database: db}, tok))
	if err == nil {
		for st.Receive() {
		}
		err = st.Err()
	}
	if connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Errorf("ExecutePipeline: %v", err)
	}
	admin := adminpbconnect.NewFirestoreAdminClient(h2c(0), x.url, connect.WithGRPC())
	_, err = admin.GetDatabase(ctx, req(&adminpb.GetDatabaseRequest{Name: db}, tok))
	if connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Errorf("FirestoreAdmin.GetDatabase: %v", err)
	}
	for _, path := range []string{"/google.firestore.v1.Firestore/NoSuchMethod", "/no.such.Service/Method"} {
		resp := rawCall(t, x.url, path, nil, 0)
		if resp.Header.Get("Grpc-Status") != "12" && resp.Trailer.Get("Grpc-Status") != "12" {
			t.Errorf("%s: status %d, grpc-status %q", path, resp.StatusCode, resp.Header.Get("Grpc-Status"))
		}
		resp.Body.Close()
	}
}

// frame is a gRPC length-prefixed message.
func frame(t *testing.T, m proto.Message) []byte {
	t.Helper()
	b, err := proto.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]byte, 5, 5+len(b))
	binary.BigEndian.PutUint32(out[1:], uint32(len(b)))
	return append(out, b...)
}

// rawCall sends a gRPC request by hand over h2c, its body the given reader (nil: empty).
func rawCall(t *testing.T, base, path string, body io.Reader, window int, header ...string) *http.Response {
	t.Helper()
	if body == nil {
		body = bytes.NewReader(nil)
	}
	r, err := http.NewRequestWithContext(t.Context(), http.MethodPost, base+path, body)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/grpc")
	r.Header.Set("Te", "trailers")
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Set(header[i], header[i+1])
	}
	resp, err := h2c(window).Do(r)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// A stream that never delivers its first message is ended at stream_first_message, rather
// than held to the server's 60 s ReadTimeout (ADR-027 §3.2).
func TestFirstMessageDeadline(t *testing.T) {
	x := newHarness(t, apitest.Options{}, docgrpc.Limits{StreamFirstMessage: 300 * time.Millisecond})
	pr, pw := io.Pipe()
	t.Cleanup(func() { pw.Close() })
	start := time.Now()
	done := make(chan error, 1)
	go func() {
		r, err := http.NewRequestWithContext(t.Context(), http.MethodPost, x.url+firestorepbconnect.FirestoreBatchGetDocumentsProcedure, pr)
		if err != nil {
			done <- err
			return
		}
		r.Header.Set("Content-Type", "application/grpc")
		r.Header.Set("Authorization", "Bearer "+x.env.JWT(t))
		resp, err := h2c(0).Do(r)
		if err != nil {
			done <- err
			return
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if s := resp.Trailer.Get("Grpc-Status"); s == "0" || s == "" && resp.Header.Get("Grpc-Status") == "0" {
			done <- errors.New("the stream succeeded")
			return
		}
		done <- nil
	}()
	select {
	case <-done:
		if d := time.Since(start); d > 5*time.Second {
			t.Errorf("the stream ended after %v", d)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a stream without its first message is still open after 10 s")
	}
}

// A client that stops reading a server stream is cut at stream_write_stall, and its stream no
// longer counts (ADR-027 §3.2).
func TestWriteStall(t *testing.T) {
	x := newHarness(t, apitest.Options{}, docgrpc.Limits{StreamWriteStall: 300 * time.Millisecond})
	big := value.String(strings.Repeat("x", 900_000))
	var names []string
	for i := range 4 {
		p := documents.Path{"big", string(rune('a' + i))}
		if _, err := x.env.Store.Commit(t.Context(), documents.DB{Project: x.env.Number, ProjectID: apitest.Project, ID: "(default)"},
			[]documents.Write{{Kind: documents.WriteUpdate, Path: p, Fields: value.Map(value.Field{Name: "s", Value: big})}}); err != nil {
			t.Fatal(err)
		}
		names = append(names, root+"/"+p.String())
	}
	body := frame(t, &pb.BatchGetDocumentsRequest{Database: db, Documents: names})
	resp := rawCall(t, x.url, firestorepbconnect.FirestoreBatchGetDocumentsProcedure, bytes.NewReader(body), 64<<10,
		"Authorization", "Bearer "+x.env.JWT(t))
	defer resp.Body.Close()
	// The client reads nothing; the server's writes stall once its window is full.
	deadline := time.Now().Add(10 * time.Second)
	for x.h.OpenStreams() > 0 {
		if time.Now().After(deadline) {
			t.Fatal("a stalled stream is still open after 10 s")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// holdingStore holds BatchGet of the documents under "hold" until its context ends.
type holdingStore struct {
	documents.Store
	held chan struct{}
}

func (s holdingStore) BatchGet(ctx context.Context, db documents.DB, paths []documents.Path, o documents.ReadOptions) ([]documents.Document, time.Time, error) {
	if len(paths) > 0 && paths[0][0] == "hold" {
		s.held <- struct{}{}
		<-ctx.Done()
		return nil, time.Time{}, ctx.Err()
	}
	return s.Store.BatchGet(ctx, db, paths, o)
}

func holding(held chan struct{}) apitest.Options {
	return apitest.Options{Store: func(s documents.Store) documents.Store { return holdingStore{s, held} }}
}

// The drain ends the open streams with UNAVAILABLE at once, and answers new ones UNAVAILABLE
// (ADR-027 §3.4).
func TestDrain(t *testing.T) {
	held := make(chan struct{}, 1)
	x := newHarness(t, holding(held), docgrpc.Limits{})
	tok := x.env.JWT(t)
	errc := make(chan error, 1)
	go func() {
		s, err := x.client.BatchGetDocuments(t.Context(), req(&pb.BatchGetDocumentsRequest{Database: db, Documents: []string{root + "/hold/x"}}, tok))
		if err == nil {
			for s.Receive() {
			}
			err = s.Err()
		}
		errc <- err
	}()
	<-held
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	start := time.Now()
	if err := x.h.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("the drain took %v", d)
	}
	wantErr(t, "open stream", <-errc, connect.CodeUnavailable, "The server is shutting down. Retry the request.")
	s, err := x.client.BatchGetDocuments(t.Context(), req(&pb.BatchGetDocumentsRequest{Database: db, Documents: []string{root + "/a/b"}}, tok))
	if err == nil {
		for s.Receive() {
		}
		err = s.Err()
	}
	wantErr(t, "new stream", err, connect.CodeUnavailable, "The server is shutting down. Retry the request.")
	// Unary calls still run until http.Server.Shutdown.
	if _, err := x.client.ListCollectionIds(t.Context(), req(&pb.ListCollectionIdsRequest{Parent: root}, tok)); err != nil {
		t.Errorf("unary during the drain: %v", err)
	}
}

// A server stream without grpc-timeout ends at server_stream_max with DEADLINE_EXCEEDED.
func TestServerStreamMax(t *testing.T) {
	held := make(chan struct{}, 1)
	x := newHarness(t, holding(held), docgrpc.Limits{ServerStreamMax: 300 * time.Millisecond})
	s, err := x.client.BatchGetDocuments(t.Context(), req(&pb.BatchGetDocumentsRequest{Database: db, Documents: []string{root + "/hold/x"}}, x.env.JWT(t)))
	if err == nil {
		for s.Receive() {
		}
		err = s.Err()
	}
	wantErr(t, "server_stream_max", err, connect.CodeDeadlineExceeded, "Deadline expired before operation could complete.")
}

// firstPageToken is the token of the first page of collection l by name, two to a page.
func firstPageToken(t *testing.T, c firestorepbconnect.FirestoreClient, tok string) string {
	t.Helper()
	res, err := c.ListDocuments(t.Context(), req(&pb.ListDocumentsRequest{Parent: root, CollectionId: "l", PageSize: 2}, tok))
	if err != nil || res.Msg.NextPageToken == "" {
		t.Fatalf("first page: %v %v", res, err)
	}
	return res.Msg.NextPageToken
}

// TestWireErrorsReplay replays the gRPC cases of f1-wire-errors.json: a database field production
// refuses, each recorded on BeginTransaction with google-cloud-resource-prefix naming (default).
func TestWireErrorsReplay(t *testing.T) {
	x := newHarness(t, apitest.Options{}, docgrpc.Limits{})
	ctx, tok := t.Context(), x.env.JWT(t)
	// gaps are the cases this server does not answer as production, with the reason.
	gaps := map[string]string{
		// Google's API-enablement check reads "bad" as a project id before Firestore parses the
		// name; Aruma has no such layer and answers the parser's INVALID_ARGUMENT.
		"grpc.database_field.bad": "Google's API-enablement check",
	}
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "test", "oracle", "documents", "corpus", "f1-wire-errors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Cases []struct {
			ID        string `json:"id"`
			Transport string `json:"transport"`
			Request   struct {
				RPC     string `json:"rpc"`
				Message struct {
					Database string `json:"database"`
				} `json:"message"`
			} `json:"request"`
			Error struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	sent := 0
	for _, c := range f.Cases {
		if c.Transport != "grpc" {
			continue
		}
		if c.Request.RPC != "google.firestore.v1.Firestore/BeginTransaction" {
			t.Fatalf("%s: rpc %s", c.ID, c.Request.RPC)
		}
		sent++
		r := req(&pb.BeginTransactionRequest{Database: c.Request.Message.Database}, tok)
		r.Header().Set("Google-Cloud-Resource-Prefix", db)
		_, err := x.client.BeginTransaction(ctx, r)
		ce, _ := errors.AsType[*connect.Error](err)
		ok := ce != nil && int(ce.Code()) == c.Error.Code && ce.Message() == c.Error.Message
		if reason, known := gaps[c.ID]; known {
			if ok {
				t.Errorf("%s: answered as production, but listed as a gap (%s)", c.ID, reason)
			}
			continue
		}
		if !ok {
			t.Errorf("%s: database %q: %v, want %d %q", c.ID, c.Request.Message.Database, err, c.Error.Code, c.Error.Message)
		}
	}
	if sent != 6 {
		t.Errorf("%d gRPC cases sent", sent)
	}
}

// A forged page token on ListDocuments with no collection id, which once panicked the store
// (F1 review M2), is refused.
func TestForgedPageToken(t *testing.T) {
	x := newHarness(t, apitest.Options{}, docgrpc.Limits{})
	ctx, tok := t.Context(), x.env.JWT(t)
	sum := sha256.New()
	for _, s := range []string{root, "", "", "0"} {
		sum.Write([]byte(s))
		sum.Write([]byte{0})
	}
	for _, after := range [][]string{{}, {"a"}, {"a", "b", "c"}, {"a", "b", "c", "d"}} {
		b, err := json.Marshal(map[string]any{"l": sum.Sum(nil), "a": after})
		if err != nil {
			t.Fatal(err)
		}
		_, err = x.client.ListDocuments(ctx, req(&pb.ListDocumentsRequest{Parent: root, PageToken: base64.RawURLEncoding.EncodeToString(b)}, tok))
		wantErr(t, fmt.Sprintf("after %q", after), err, connect.CodeInvalidArgument, "Invalid page token.")
	}
}
