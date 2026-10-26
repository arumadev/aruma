package documents

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	pb "cloud.google.com/go/firestore/apiv1/firestorepb"
	"google.golang.org/api/iterator"
	"google.golang.org/genproto/googleapis/type/latlng"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TestAuth: the key file's JWT is what admits the client; without it the server refuses, as
// production does a request without credentials (f1-resource-names.json).
func TestAuth(t *testing.T) {
	check(t, "without credentials", func(t T) {
		c := newRaw(t, false)
		defer c.Close()
		_, err := c.GetDocument(t.Context(), &pb.GetDocumentRequest{Name: root() + "/auth/x"})
		wantCode(t, err, codes.PermissionDenied, "Missing or insufficient permissions.")
	})
	check(t, "with the key file", func(t T) {
		c := newRaw(t, true)
		defer c.Close()
		_, err := c.GetDocument(t.Context(), &pb.GetDocumentRequest{Name: root() + "/auth/x"})
		wantCode(t, err, codes.NotFound, fmt.Sprintf("Document %q not found.", root()+"/auth/x"))
	})
}

// TestRawValues writes every value production recorded with CreateDocument and reads it back
// with GetDocument and BatchGetDocuments: the value must come back as production stored it, bit
// for bit, and the values production refused must be refused with its code and message.
func TestRawValues(t *testing.T) {
	c := newRaw(t, true)
	defer c.Close()
	values := loadValues(t)
	var stored []corpusValue
	for _, v := range values {
		if v.Stored != nil {
			stored = append(stored, v)
		}
		check(t, v.ID, func(t T) {
			doc, err := c.CreateDocument(t.Context(), &pb.CreateDocumentRequest{
				Parent: root(), CollectionId: "values", DocumentId: v.ID,
				Document: &pb.Document{Fields: map[string]*pb.Value{"v": v.Sent}},
			})
			if v.Stored == nil {
				wantCode(t, err, codes.InvalidArgument, v.Error.Message)
				return
			}
			mustOK(t, err, "CreateDocument")
			if got := doc.GetFields()["v"]; !sameValue(got, v.Stored) {
				t.Errorf("CreateDocument returned\n%s\nwant\n%s", show(got), show(v.Stored))
			}
			got, err := c.GetDocument(t.Context(), &pb.GetDocumentRequest{Name: root() + "/values/" + v.ID})
			mustOK(t, err, "GetDocument")
			if !sameValue(got.GetFields()["v"], v.Stored) {
				t.Errorf("GetDocument returned\n%s\nwant\n%s", show(got.GetFields()["v"]), show(v.Stored))
			}
		})
	}
	check(t, "batch get of every value", func(t T) {
		req := &pb.BatchGetDocumentsRequest{Database: database()}
		want := map[string]*pb.Value{}
		for _, v := range stored {
			name := root() + "/values/" + v.ID
			req.Documents = append(req.Documents, name)
			want[name] = v.Stored
		}
		res, err := batchGet(t.Context(), c, req)
		mustOK(t, err, "BatchGetDocuments")
		if len(res) != len(want) {
			t.Fatalf("%d responses for %d documents", len(res), len(want))
		}
		for _, r := range res {
			f := r.GetFound()
			if f == nil {
				t.Errorf("missing: %s", r.GetMissing())
				continue
			}
			if !sameValue(f.GetFields()["v"], want[f.GetName()]) {
				t.Errorf("%s:\n%s\nwant\n%s", f.GetName(), show(f.GetFields()["v"]), show(want[f.GetName()]))
			}
			if r.GetReadTime() == nil {
				t.Errorf("%s: no read_time", f.GetName())
			}
		}
	})
}

func TestRawCreate(t *testing.T) {
	c := newRaw(t, true)
	defer c.Close()
	check(t, "with an id", func(t T) {
		doc, err := c.CreateDocument(t.Context(), &pb.CreateDocumentRequest{
			Parent: root(), CollectionId: "create", DocumentId: "c1", Document: &pb.Document{Fields: fields("a", 1, "s", "x")},
		})
		mustOK(t, err, "CreateDocument")
		if doc.GetName() != root()+"/create/c1" {
			t.Errorf("name %s", doc.GetName())
		}
		if doc.GetCreateTime() == nil || !sameTime(doc.GetCreateTime(), doc.GetUpdateTime()) {
			t.Errorf("createTime %v, updateTime %v", doc.GetCreateTime(), doc.GetUpdateTime())
		}
	})
	check(t, "the document exists", func(t T) {
		_, err := c.CreateDocument(t.Context(), &pb.CreateDocumentRequest{
			Parent: root(), CollectionId: "create", DocumentId: "c1", Document: &pb.Document{},
		})
		wantCode(t, err, codes.AlreadyExists, "Document already exists: "+root()+"/create/c1")
	})
	check(t, "without an id", func(t T) {
		doc, err := c.CreateDocument(t.Context(), &pb.CreateDocumentRequest{
			Parent: root(), CollectionId: "create", Document: &pb.Document{Fields: fields("auto", true)},
		})
		mustOK(t, err, "CreateDocument")
		id := strings.TrimPrefix(doc.GetName(), root()+"/create/")
		if len(id) != 20 || strings.Trim(id, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789") != "" {
			t.Errorf("automatic id %q", id)
		}
	})
	check(t, "a reserved id", func(t T) {
		_, err := c.CreateDocument(t.Context(), &pb.CreateDocumentRequest{
			Parent: root(), CollectionId: "create", DocumentId: "__x__", Document: &pb.Document{},
		})
		wantCode(t, err, codes.InvalidArgument, `Resource id "__x__" is invalid because it is reserved.`)
	})
	check(t, "under a missing parent", func(t T) {
		_, err := c.CreateDocument(t.Context(), &pb.CreateDocumentRequest{
			Parent: root() + "/create/mp", CollectionId: "sub", DocumentId: "x", Document: &pb.Document{Fields: fields("n", 1)},
		})
		mustOK(t, err, "CreateDocument")
	})
	check(t, "with a mask", func(t T) {
		doc, err := c.CreateDocument(t.Context(), &pb.CreateDocumentRequest{
			Parent: root(), CollectionId: "create", DocumentId: "c2", Document: &pb.Document{Fields: fields("a", 1, "b", 2)},
			Mask: &pb.DocumentMask{FieldPaths: []string{"a"}},
		})
		mustOK(t, err, "CreateDocument")
		if !sameFields(doc.GetFields(), fields("a", 1)) {
			t.Errorf("fields %v", doc.GetFields())
		}
	})
}

func TestRawGet(t *testing.T) {
	c := newRaw(t, true)
	defer c.Close()
	_, err := c.CreateDocument(context.Background(), &pb.CreateDocumentRequest{
		Parent: root(), CollectionId: "get", DocumentId: "g1",
		Document: &pb.Document{Fields: fields("a", 1, "b", fields("c", 2, "d", 3), "s", "x")},
	})
	if err != nil {
		t.Fatal(err)
	}
	check(t, "a document", func(t T) {
		doc, err := c.GetDocument(t.Context(), &pb.GetDocumentRequest{Name: root() + "/get/g1"})
		mustOK(t, err, "GetDocument")
		if !sameFields(doc.GetFields(), fields("a", 1, "b", fields("c", 2, "d", 3), "s", "x")) {
			t.Errorf("fields %v", doc.GetFields())
		}
	})
	check(t, "with a mask", func(t T) {
		doc, err := c.GetDocument(t.Context(), &pb.GetDocumentRequest{Name: root() + "/get/g1", Mask: &pb.DocumentMask{FieldPaths: []string{"b.c", "s"}}})
		mustOK(t, err, "GetDocument")
		if !sameFields(doc.GetFields(), fields("b", fields("c", 2), "s", "x")) {
			t.Errorf("fields %v", doc.GetFields())
		}
	})
	check(t, "a missing document", func(t T) {
		_, err := c.GetDocument(t.Context(), &pb.GetDocumentRequest{Name: root() + "/get/nope"})
		wantCode(t, err, codes.NotFound, fmt.Sprintf("Document %q not found.", root()+"/get/nope"))
	})
	check(t, "a read time in the future", func(t T) {
		_, err := c.GetDocument(t.Context(), &pb.GetDocumentRequest{Name: root() + "/get/g1",
			ConsistencySelector: &pb.GetDocumentRequest_ReadTime{ReadTime: timestamppb.New(time.Now().Add(time.Hour).Truncate(time.Second))}})
		wantCode(t, err, codes.InvalidArgument, "The requested 'read_time' cannot be in the future.")
	})
}

func TestRawUpdate(t *testing.T) {
	c := newRaw(t, true)
	defer c.Close()
	name := root() + "/update/u1"
	update := func(t T, f map[string]*pb.Value, mask []string, pre *pb.Precondition) (*pb.Document, error) {
		req := &pb.UpdateDocumentRequest{Document: &pb.Document{Name: name, Fields: f}, CurrentDocument: pre}
		if mask != nil {
			req.UpdateMask = &pb.DocumentMask{FieldPaths: mask}
		}
		return c.UpdateDocument(t.Context(), req)
	}
	var first *pb.Document
	check(t, "creates a missing document", func(t T) {
		var err error
		first, err = update(t, fields("a", 1, "b", 2), nil, nil)
		mustOK(t, err, "UpdateDocument")
	})
	check(t, "replaces every field without a mask", func(t T) {
		doc, err := update(t, fields("c", 3), nil, nil)
		mustOK(t, err, "UpdateDocument")
		if !sameFields(doc.GetFields(), fields("c", 3)) {
			t.Errorf("fields %v", doc.GetFields())
		}
	})
	check(t, "merges the fields in the mask", func(t T) {
		doc, err := update(t, fields("a", 10, "z", 9, "x", fields("y", 1, "q", 2)), []string{"a", "x.y", "c"}, nil)
		mustOK(t, err, "UpdateDocument")
		if !sameFields(doc.GetFields(), fields("a", 10, "x", fields("y", 1))) {
			t.Errorf("fields %v", doc.GetFields())
		}
	})
	check(t, "exists=true on the document", func(t T) {
		_, err := update(t, fields("a", 11), []string{"a"}, exists(true))
		mustOK(t, err, "UpdateDocument")
	})
	check(t, "exists=false on the document", func(t T) {
		_, err := update(t, fields("a", 12), []string{"a"}, exists(false))
		wantCode(t, err, codes.AlreadyExists, "Document already exists: "+name)
	})
	check(t, "exists=true on a missing document", func(t T) {
		_, err := c.UpdateDocument(t.Context(), &pb.UpdateDocumentRequest{
			Document: &pb.Document{Name: root() + "/update/u-missing", Fields: fields("a", 1)}, CurrentDocument: exists(true)})
		wantCode(t, err, codes.NotFound, "No document to update: "+root()+"/update/u-missing")
	})
	check(t, "an old update time", func(t T) {
		cur, err := c.GetDocument(t.Context(), &pb.GetDocumentRequest{Name: name})
		mustOK(t, err, "GetDocument")
		if first == nil {
			t.Fatal("no first write")
		}
		_, err = update(t, fields("a", 13), []string{"a"}, &pb.Precondition{ConditionType: &pb.Precondition_UpdateTime{UpdateTime: first.GetUpdateTime()}})
		wantCode(t, err, codes.FailedPrecondition, fmt.Sprintf("the stored version (%d) does not match the required base version (%d)",
			cur.GetUpdateTime().AsTime().UnixMicro(), first.GetUpdateTime().AsTime().UnixMicro()))
	})
	check(t, "the current update time", func(t T) {
		cur, err := c.GetDocument(t.Context(), &pb.GetDocumentRequest{Name: name})
		mustOK(t, err, "GetDocument")
		doc, err := update(t, fields("a", 14), []string{"a"}, &pb.Precondition{ConditionType: &pb.Precondition_UpdateTime{UpdateTime: cur.GetUpdateTime()}})
		mustOK(t, err, "UpdateDocument")
		if !doc.GetUpdateTime().AsTime().After(cur.GetUpdateTime().AsTime()) {
			t.Errorf("updateTime %v after %v", doc.GetUpdateTime().AsTime(), cur.GetUpdateTime().AsTime())
		}
		if !sameTime(doc.GetCreateTime(), cur.GetCreateTime()) {
			t.Errorf("createTime changed: %v, was %v", doc.GetCreateTime(), cur.GetCreateTime())
		}
	})
}

func TestRawDelete(t *testing.T) {
	c := newRaw(t, true)
	defer c.Close()
	create := func(t T, id string) *pb.Document {
		doc, err := c.CreateDocument(t.Context(), &pb.CreateDocumentRequest{Parent: root(), CollectionId: "delete", DocumentId: id, Document: &pb.Document{Fields: fields("n", 1)}})
		mustOK(t, err, "CreateDocument "+id)
		return doc
	}
	del := func(t T, id string, pre *pb.Precondition) error {
		return c.DeleteDocument(t.Context(), &pb.DeleteDocumentRequest{Name: root() + "/delete/" + id, CurrentDocument: pre})
	}
	check(t, "a missing document", func(t T) { mustOK(t, del(t, "d-missing", nil), "DeleteDocument") })
	check(t, "exists=true on a missing document", func(t T) {
		wantCode(t, del(t, "d-missing", exists(true)), codes.NotFound, "No document to update: "+root()+"/delete/d-missing")
	})
	check(t, "exists=false on a missing document", func(t T) { mustOK(t, del(t, "d-missing", exists(false)), "DeleteDocument") })
	check(t, "exists=false on a document", func(t T) {
		create(t, "d1")
		wantCode(t, del(t, "d1", exists(false)), codes.AlreadyExists, "Document already exists: "+root()+"/delete/d1")
	})
	check(t, "a wrong update time", func(t T) {
		doc := create(t, "d2")
		old := timestamppb.New(time.Date(2001, 1, 1, 0, 0, 0, 1000, time.UTC))
		err := del(t, "d2", &pb.Precondition{ConditionType: &pb.Precondition_UpdateTime{UpdateTime: old}})
		wantCode(t, err, codes.FailedPrecondition, fmt.Sprintf("the stored version (%d) does not match the required base version (978307200000001)",
			doc.GetUpdateTime().AsTime().UnixMicro()))
	})
	check(t, "the current update time", func(t T) {
		doc := create(t, "d3")
		mustOK(t, del(t, "d3", &pb.Precondition{ConditionType: &pb.Precondition_UpdateTime{UpdateTime: doc.GetUpdateTime()}}), "DeleteDocument")
		_, err := c.GetDocument(t.Context(), &pb.GetDocumentRequest{Name: root() + "/delete/d3"})
		wantCode(t, err, codes.NotFound, "")
	})
	check(t, "exists=true on a document", func(t T) {
		create(t, "d4")
		mustOK(t, del(t, "d4", exists(true)), "DeleteDocument")
	})
}

func TestRawBatchGet(t *testing.T) {
	c := newRaw(t, true)
	defer c.Close()
	for _, id := range []string{"g1", "g2", "g3"} {
		_, err := c.CreateDocument(context.Background(), &pb.CreateDocumentRequest{Parent: root(), CollectionId: "batch", DocumentId: id,
			Document: &pb.Document{Fields: fields("a", 1, "b", id)}})
		if err != nil {
			t.Fatal(err)
		}
	}
	name := func(id string) string { return root() + "/batch/" + id }
	check(t, "found and missing", func(t T) {
		ids := []string{"g3", "nope1", "g1", "nope2", "g2"}
		req := &pb.BatchGetDocumentsRequest{Database: database()}
		for _, id := range ids {
			req.Documents = append(req.Documents, name(id))
		}
		res, err := batchGet(t.Context(), c, req)
		mustOK(t, err, "BatchGetDocuments")
		var found, missing []string
		for _, r := range res {
			if f := r.GetFound(); f != nil {
				found = append(found, f.GetName())
			} else {
				missing = append(missing, r.GetMissing())
			}
		}
		slices.Sort(found)
		slices.Sort(missing)
		if !slices.Equal(found, []string{name("g1"), name("g2"), name("g3")}) || !slices.Equal(missing, []string{name("nope1"), name("nope2")}) {
			t.Errorf("found %v, missing %v", found, missing)
		}
	})
	check(t, "with a mask", func(t T) {
		res, err := batchGet(t.Context(), c, &pb.BatchGetDocumentsRequest{Database: database(), Documents: []string{name("g1")},
			Mask: &pb.DocumentMask{FieldPaths: []string{"a"}}})
		mustOK(t, err, "BatchGetDocuments")
		if len(res) != 1 || !sameFields(res[0].GetFound().GetFields(), fields("a", 1)) {
			t.Errorf("responses %v", res)
		}
	})
	check(t, "a document of another project", func(t T) {
		_, err := batchGet(t.Context(), c, &pb.BatchGetDocumentsRequest{Database: database(),
			Documents: []string{"projects/aruma-ref-no-such-project/databases/(default)/documents/batch/g1"}})
		wantCode(t, err, codes.InvalidArgument, fmt.Sprintf("The request was for database '%s' but was attempting to access database 'projects/aruma-ref-no-such-project/databases/(default)'", database()))
	})
}

func TestRawListDocuments(t *testing.T) {
	c := newRaw(t, true)
	defer c.Close()
	for i, id := range []string{"a", "b", "c", "d", "e"} {
		_, err := c.CreateDocument(context.Background(), &pb.CreateDocumentRequest{Parent: root(), CollectionId: "list", DocumentId: id,
			Document: &pb.Document{Fields: fields("n", 5-i)}})
		if err != nil {
			t.Fatal(err)
		}
	}
	// A missing document with a subcollection, which showMissing lists.
	_, err := c.CreateDocument(context.Background(), &pb.CreateDocumentRequest{Parent: root() + "/list/m", CollectionId: "sub", DocumentId: "x", Document: &pb.Document{}})
	if err != nil {
		t.Fatal(err)
	}
	list := func(t T, req *pb.ListDocumentsRequest) []string {
		t.Helper()
		req.Parent, req.CollectionId = root(), "list"
		it := c.ListDocuments(t.Context(), req)
		var ids []string
		for {
			d, err := it.Next()
			if err == iterator.Done {
				return ids
			}
			mustOK(t, err, "ListDocuments")
			ids = append(ids, strings.TrimPrefix(d.GetName(), root()+"/list/"))
		}
	}
	check(t, "in pages of two", func(t T) {
		if got := list(t, &pb.ListDocumentsRequest{PageSize: 2}); !slices.Equal(got, []string{"a", "b", "c", "d", "e"}) {
			t.Errorf("ids %v", got)
		}
	})
	check(t, "ordered by a field", func(t T) {
		if got := list(t, &pb.ListDocumentsRequest{OrderBy: "n"}); !slices.Equal(got, []string{"e", "d", "c", "b", "a"}) {
			t.Errorf("ids %v", got)
		}
	})
	check(t, "showMissing", func(t T) {
		if got := list(t, &pb.ListDocumentsRequest{ShowMissing: true, PageSize: 2}); !slices.Equal(got, []string{"a", "b", "c", "d", "e", "m"}) {
			t.Errorf("ids %v", got)
		}
	})
	check(t, "a negative page size", func(t T) {
		_, err := c.ListDocuments(t.Context(), &pb.ListDocumentsRequest{Parent: root(), CollectionId: "list", PageSize: -1}).Next()
		wantCode(t, err, codes.InvalidArgument, "Page size must be nonnegative.")
	})
}

func TestRawListCollectionIds(t *testing.T) {
	c := newRaw(t, true)
	defer c.Close()
	for _, sub := range []string{"s5", "s1", "s3", "s2", "s4"} {
		_, err := c.CreateDocument(context.Background(), &pb.CreateDocumentRequest{Parent: root() + "/lcids/p", CollectionId: sub, DocumentId: "x", Document: &pb.Document{}})
		if err != nil {
			t.Fatal(err)
		}
	}
	ids := func(t T, parent string, size int32) []string {
		t.Helper()
		it := c.ListCollectionIds(t.Context(), &pb.ListCollectionIdsRequest{Parent: parent, PageSize: size})
		var out []string
		for {
			id, err := it.Next()
			if err == iterator.Done {
				return out
			}
			mustOK(t, err, "ListCollectionIds")
			out = append(out, id)
		}
	}
	check(t, "of a document", func(t T) {
		if got := ids(t, root()+"/lcids/p", 2); !slices.Equal(got, []string{"s1", "s2", "s3", "s4", "s5"}) {
			t.Errorf("ids %v", got)
		}
	})
	check(t, "of the root", func(t T) {
		if got := ids(t, root(), 0); !slices.Contains(got, "lcids") {
			t.Errorf("ids %v", got)
		}
	})
	check(t, "of a document without subcollections", func(t T) {
		if got := ids(t, root()+"/lcids/never", 0); len(got) != 0 {
			t.Errorf("ids %v", got)
		}
	})
	check(t, "an invalid page token", func(t T) {
		_, err := c.ListCollectionIds(t.Context(), &pb.ListCollectionIdsRequest{Parent: root() + "/lcids/p", PageToken: "abc"}).Next()
		wantCode(t, err, codes.InvalidArgument, "invalid page token")
	})
}

// typed lists values of values-order.json and what the Go SDK decodes each to.
var typed = map[string]any{
	"null":            nil,
	"bool-true":       true,
	"n-i-max":         int64(math.MaxInt64),
	"n-i-min":         int64(math.MinInt64),
	"n-i-2p53-plus-1": int64(1<<53 + 1),
	"n-d-max":         math.MaxFloat64,
	"n-d-min":         math.SmallestNonzeroFloat64,
	"n-pos-inf":       math.Inf(1),
	"n-neg-inf":       math.Inf(-1),
	"n-d-1.5":         1.5,
	"s-emoji":         nil, // compared with the stored string below
	"s-e-acute":       nil,
	"s-u-ffff":        nil,
	"b-ff00":          []byte{0xff, 0x00},
	"b-empty":         []byte{},
	"g-s90-w180":      &latlng.LatLng{Latitude: -90, Longitude: -180},
	"v-1-2-3":         firestore.Vector64{1, 2, 3},
	"a-i-1-s-a":       []any{int64(1), "a"},
	"m-a-1-b-1":       nil,
	"t-micros":        nil,
	"t-max":           nil,
	"t-min":           nil,
	"r-c-a":           nil,
}

// TestSDKReads reads through firestore.NewClient: DocumentRef.Get and Client.GetAll
// (BatchGetDocuments), CollectionRef.DocumentRefs (ListDocuments) and Collections
// (ListCollectionIds), and decodes every kind of value into Go.
func TestSDKReads(t *testing.T) {
	raw := newRaw(t, true)
	defer raw.Close()
	values := loadValues(t)
	byID := map[string]corpusValue{}
	for _, v := range values {
		byID[v.ID] = v
		if v.Stored != nil {
			_, err := raw.CreateDocument(context.Background(), &pb.CreateDocumentRequest{Parent: root(), CollectionId: "sdk-values", DocumentId: v.ID,
				Document: &pb.Document{Fields: map[string]*pb.Value{"v": v.Sent}}})
			if err != nil {
				t.Fatalf("CreateDocument %s: %v", v.ID, err)
			}
		}
	}
	_, err := raw.CreateDocument(context.Background(), &pb.CreateDocumentRequest{Parent: root() + "/sdk-values/null", CollectionId: "sub", DocumentId: "x", Document: &pb.Document{}})
	if err != nil {
		t.Fatal(err)
	}
	c := newClient(t)
	defer c.Close()

	check(t, "get", func(t T) {
		snap, err := c.Doc("sdk-values/bool-true").Get(t.Context())
		mustOK(t, err, "Get")
		if !snap.Exists() || snap.Data()["v"] != true || snap.CreateTime.IsZero() || snap.UpdateTime.IsZero() || snap.ReadTime.IsZero() {
			t.Errorf("snapshot %+v", snap)
		}
	})
	check(t, "get a missing document", func(t T) {
		snap, err := c.Doc("sdk-values/nope").Get(t.Context())
		wantCode(t, err, codes.NotFound, "")
		if snap == nil || snap.Exists() || snap.ReadTime.IsZero() {
			t.Errorf("snapshot %+v", snap)
		}
	})
	check(t, "get all", func(t T) {
		refs := []*firestore.DocumentRef{c.Doc("sdk-values/n-i-1"), c.Doc("sdk-values/nope"), c.Doc("sdk-values/s-a")}
		snaps, err := c.GetAll(t.Context(), refs)
		mustOK(t, err, "GetAll")
		if len(snaps) != 3 || !snaps[0].Exists() || snaps[1].Exists() || !snaps[2].Exists() {
			t.Fatalf("snapshots %v", snaps)
		}
		if snaps[0].Data()["v"] != int64(1) || snaps[2].Data()["v"] != "a" || snaps[1].Ref.ID != "nope" {
			t.Errorf("data %v %v", snaps[0].Data(), snaps[2].Data())
		}
	})
	check(t, "every type", func(t T) {
		var refs []*firestore.DocumentRef
		for id := range typed {
			refs = append(refs, c.Doc("sdk-values/"+id))
		}
		snaps, err := c.GetAll(t.Context(), refs)
		mustOK(t, err, "GetAll")
		for _, s := range snaps {
			if !s.Exists() {
				t.Errorf("%s does not exist", s.Ref.ID)
				continue
			}
			got, err := s.DataAt("v")
			if err != nil {
				t.Errorf("%s: %v", s.Ref.ID, err)
				continue
			}
			want := typed[s.Ref.ID]
			stored := byID[s.Ref.ID].Stored
			switch s.Ref.ID {
			case "s-emoji", "s-e-acute", "s-u-ffff":
				want = stored.GetStringValue()
			case "m-a-1-b-1":
				f := stored.GetMapValue().GetFields()
				want = map[string]any{"a": f["a"].GetIntegerValue(), "b": f["b"].GetIntegerValue()}
			case "t-micros", "t-max", "t-min":
				want = stored.GetTimestampValue().AsTime()
			case "r-c-a":
				r, ok := got.(*firestore.DocumentRef)
				if !ok || r.Path != stored.GetReferenceValue() {
					t.Errorf("r-c-a: %#v, want a reference to %s", got, stored.GetReferenceValue())
				}
				continue
			}
			if !sameGo(got, want) {
				t.Errorf("%s: got %#v, want %#v", s.Ref.ID, got, want)
			}
		}
		for _, id := range []string{"n-nan", "n-d-neg-zero", "n-d-zero"} {
			s, err := c.Doc("sdk-values/" + id).Get(t.Context())
			mustOK(t, err, "Get "+id)
			f, ok := s.Data()["v"].(float64)
			switch {
			case !ok:
				t.Errorf("%s: %#v", id, s.Data()["v"])
			case id == "n-nan" && !math.IsNaN(f), id == "n-d-neg-zero" && (f != 0 || !math.Signbit(f)), id == "n-d-zero" && (f != 0 || math.Signbit(f)):
				t.Errorf("%s: %v (sign bit %v)", id, f, math.Signbit(f))
			}
		}
	})
	check(t, "list documents", func(t T) {
		refs, err := c.Collection("sdk-values").DocumentRefs(t.Context()).GetAll()
		mustOK(t, err, "DocumentRefs")
		want := 0
		for _, v := range values {
			if v.Stored != nil {
				want++
			}
		}
		if len(refs) != want {
			t.Errorf("%d references, want %d", len(refs), want)
		}
	})
	check(t, "list collections", func(t T) {
		cols, err := c.Collections(t.Context()).GetAll()
		mustOK(t, err, "Collections")
		if !slices.ContainsFunc(cols, func(c *firestore.CollectionRef) bool { return c.ID == "sdk-values" }) {
			t.Errorf("collections %v", cols)
		}
		sub, err := c.Doc("sdk-values/null").Collections(t.Context()).GetAll()
		mustOK(t, err, "DocumentRef.Collections")
		if len(sub) != 1 || sub[0].ID != "sub" {
			t.Errorf("subcollections %v", sub)
		}
	})
}

// TestSDKWrites writes through firestore.NewClient. Every write of the Go SDK is a Commit,
// which F2 serves: these are listed in the expected-failure list until then.
func TestSDKWrites(t *testing.T) {
	c := newClient(t)
	defer c.Close()
	col := c.Collection("sdk-writes")
	check(t, "create", func(t T) {
		_, err := col.Doc("w1").Create(t.Context(), map[string]any{"a": 1})
		mustOK(t, err, "Create")
		_, err = col.Doc("w1").Create(t.Context(), map[string]any{"a": 2})
		wantCode(t, err, codes.AlreadyExists, "Document already exists: "+root()+"/sdk-writes/w1")
	})
	check(t, "set and merge", func(t T) {
		_, err := col.Doc("w2").Set(t.Context(), map[string]any{"a": 1, "b": map[string]any{"c": 2}})
		mustOK(t, err, "Set")
		_, err = col.Doc("w2").Set(t.Context(), map[string]any{"b": map[string]any{"d": 3}}, firestore.MergeAll)
		mustOK(t, err, "Set MergeAll")
		s, err := col.Doc("w2").Get(t.Context())
		mustOK(t, err, "Get")
		if !sameGo(s.Data(), map[string]any{"a": int64(1), "b": map[string]any{"c": int64(2), "d": int64(3)}}) {
			t.Errorf("data %v", s.Data())
		}
	})
	check(t, "update with preconditions", func(t T) {
		wr, err := col.Doc("w3").Set(t.Context(), map[string]any{"a": 1})
		mustOK(t, err, "Set")
		wr2, err := col.Doc("w3").Update(t.Context(), []firestore.Update{{Path: "a", Value: 2}}, firestore.LastUpdateTime(wr.UpdateTime))
		mustOK(t, err, "Update")
		_, err = col.Doc("w3").Update(t.Context(), []firestore.Update{{Path: "a", Value: 3}}, firestore.LastUpdateTime(wr.UpdateTime))
		wantCode(t, err, codes.FailedPrecondition, fmt.Sprintf("the stored version (%d) does not match the required base version (%d)",
			wr2.UpdateTime.UnixMicro(), wr.UpdateTime.UnixMicro()))
		_, err = col.Doc("w-missing").Update(t.Context(), []firestore.Update{{Path: "a", Value: 1}})
		wantCode(t, err, codes.NotFound, "No document to update: "+root()+"/sdk-writes/w-missing")
	})
	check(t, "delete with preconditions", func(t T) {
		_, err := col.Doc("w4").Set(t.Context(), map[string]any{"a": 1})
		mustOK(t, err, "Set")
		_, err = col.Doc("w4").Delete(t.Context(), firestore.Exists)
		mustOK(t, err, "Delete Exists")
		_, err = col.Doc("w4").Delete(t.Context(), firestore.Exists)
		wantCode(t, err, codes.NotFound, "No document to update: "+root()+"/sdk-writes/w4")
		_, err = col.Doc("w4").Delete(t.Context())
		mustOK(t, err, "Delete of a missing document")
	})
	check(t, "batch", func(t T) {
		b := c.Batch()
		b.Set(col.Doc("b1"), map[string]any{"a": 1})
		b.Create(col.Doc("b2"), map[string]any{"a": 2})
		b.Delete(col.Doc("b3"))
		res, err := b.Commit(t.Context())
		mustOK(t, err, "WriteBatch.Commit")
		if len(res) != 3 {
			t.Errorf("%d results", len(res))
		}
	})
	check(t, "every type", func(t T) {
		ref := c.Doc("sdk-values/nope") // a reference value
		ts := time.Date(2026, 10, 26, 12, 34, 56, 123456000, time.UTC)
		data := map[string]any{
			"null": nil, "true": true, "false": false,
			"max": int64(math.MaxInt64), "min": int64(math.MinInt64),
			"nan": math.NaN(), "negzero": math.Copysign(0, -1), "zero": 0.0, "inf": math.Inf(1), "dmax": math.MaxFloat64,
			"unicode": "héllo \u0000 \uffff 😀 \ue000", "bytes": []byte{0, 1, 0xfe, 0xff},
			"ref": ref, "geo": &latlng.LatLng{Latitude: -90, Longitude: 180},
			"array":  []any{int64(1), "a", map[string]any{"x": []any{true, nil}, "empty": []any{}}},
			"map":    map[string]any{"a": map[string]any{"b": map[string]any{"c": int64(1)}}, "ü 😀": "unicode key"},
			"vector": firestore.Vector64{1, -0.5, 3e300},
			"ts":     ts, "tsMin": time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC), "tsMax": time.Date(9999, 12, 31, 23, 59, 59, 999999000, time.UTC),
		}
		_, err := c.Doc("sdk-writes/types").Set(t.Context(), data)
		mustOK(t, err, "Set")
		s, err := c.Doc("sdk-writes/types").Get(t.Context())
		mustOK(t, err, "Get")
		got := s.Data()
		for k, want := range data {
			switch k {
			case "nan":
				if f, ok := got[k].(float64); !ok || !math.IsNaN(f) {
					t.Errorf("%s: %#v", k, got[k])
				}
			case "negzero":
				if f, ok := got[k].(float64); !ok || f != 0 || !math.Signbit(f) {
					t.Errorf("%s: %#v", k, got[k])
				}
			case "ref":
				if r, ok := got[k].(*firestore.DocumentRef); !ok || r.Path != ref.Path {
					t.Errorf("%s: %#v", k, got[k])
				}
			default:
				if !sameGo(got[k], want) {
					t.Errorf("%s: got %#v, want %#v", k, got[k], want)
				}
			}
		}
	})
}

// sameGo compares decoded Go values; float64s bit for bit, times as instants.
func sameGo(a, b any) bool {
	switch x := a.(type) {
	case float64:
		y, ok := b.(float64)
		return ok && sameFloat(x, y)
	case time.Time:
		y, ok := b.(time.Time)
		return ok && x.Equal(y)
	case []byte:
		y, ok := b.([]byte)
		return ok && string(x) == string(y)
	case []any:
		y, ok := b.([]any)
		return ok && slices.EqualFunc(x, y, sameGo)
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, v := range x {
			w, ok := y[k]
			if !ok || !sameGo(v, w) {
				return false
			}
		}
		return true
	case firestore.Vector64:
		y, ok := b.(firestore.Vector64)
		return ok && slices.EqualFunc(x, y, sameFloat)
	case *latlng.LatLng:
		y, ok := b.(*latlng.LatLng)
		return ok && sameFloat(x.GetLatitude(), y.GetLatitude()) && sameFloat(x.GetLongitude(), y.GetLongitude())
	}
	return reflect.DeepEqual(a, b)
}

func sameTime(a, b *timestamppb.Timestamp) bool {
	return a.GetSeconds() == b.GetSeconds() && a.GetNanos() == b.GetNanos()
}
