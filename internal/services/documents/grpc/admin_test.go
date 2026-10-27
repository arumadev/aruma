package grpc_test

import (
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	"github.com/arumadev/aruma/internal/services/documents/api/apitest"
	documents "github.com/arumadev/aruma/internal/services/documents/core"
	docgrpc "github.com/arumadev/aruma/internal/services/documents/grpc"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/adminpb"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/adminpb/adminpbconnect"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/longrunningpb"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/longrunningpb/longrunningpbconnect"
)

// The Admin API over gRPC: FirestoreAdmin and google.longrunning.Operations, as gcloud and the
// generated admin clients call them; the same service layer as REST (rest/admin_test.go).

func adminClients(x *harness) (adminpbconnect.FirestoreAdminClient, longrunningpbconnect.OperationsClient) {
	return adminpbconnect.NewFirestoreAdminClient(h2c(0), x.url, connect.WithGRPC()),
		longrunningpbconnect.NewOperationsClient(h2c(0), x.url, connect.WithGRPC())
}

func TestAdminGRPC(t *testing.T) {
	x := newHarness(t, apitest.Options{}, docgrpc.Limits{})
	admin, ops := adminClients(x)
	ctx, tok := t.Context(), x.env.JWT(t)

	d, err := admin.GetDatabase(ctx, req(&adminpb.GetDatabaseRequest{Name: db}, tok))
	if err != nil || d.Msg.GetType() != adminpb.Database_FIRESTORE_NATIVE || d.Msg.GetDatabaseEdition() != adminpb.Database_STANDARD ||
		!d.Msg.GetFreeTier() || d.Msg.GetName() != db {
		t.Fatalf("GetDatabase: %v %v", d, err)
	}
	l, err := admin.ListDatabases(ctx, req(&adminpb.ListDatabasesRequest{Parent: "projects/" + apitest.Project}, tok))
	if err != nil || len(l.Msg.GetDatabases()) != 1 {
		t.Errorf("ListDatabases: %v %v", l, err)
	}
	_, err = admin.GetDatabase(ctx, req(&adminpb.GetDatabaseRequest{Name: "projects/" + apitest.Project + "/databases/second"}, tok))
	wantErr(t, "a missing database", err, connect.CodeNotFound, "Project '"+apitest.Project+"' or database 'second' does not exist.")

	group := db + "/collectionGroups/cities"
	op, err := admin.CreateIndex(ctx, req(&adminpb.CreateIndexRequest{Parent: group, Index: &adminpb.Index{
		QueryScope: adminpb.Index_COLLECTION_GROUP,
		Fields: []*adminpb.Index_IndexField{
			{FieldPath: "tags", ValueMode: &adminpb.Index_IndexField_ArrayConfig_{ArrayConfig: adminpb.Index_IndexField_CONTAINS}},
			{FieldPath: "rank", ValueMode: &adminpb.Index_IndexField_Order_{Order: adminpb.Index_IndexField_ASCENDING}},
		},
	}}, tok))
	if err != nil || op.Msg.GetDone() {
		t.Fatalf("CreateIndex: %v %v", op, err)
	}
	md := &adminpb.IndexOperationMetadata{}
	if err := op.Msg.GetMetadata().UnmarshalTo(md); err != nil {
		t.Fatal(err)
	}
	if err := x.env.Store.BuildIndexes(ctx, documents.IndexBuildOptions{}); err != nil {
		t.Fatal(err)
	}
	ix, err := admin.GetIndex(ctx, req(&adminpb.GetIndexRequest{Name: md.GetIndex()}, tok))
	if err != nil || ix.Msg.GetState() != adminpb.Index_READY || len(ix.Msg.GetFields()) != 3 || ix.Msg.GetDensity() != adminpb.Index_SPARSE_ALL {
		t.Errorf("GetIndex: %v %v", ix, err)
	}
	list, err := admin.ListIndexes(ctx, req(&adminpb.ListIndexesRequest{Parent: db + "/collectionGroups/-"}, tok))
	if err != nil || len(list.Msg.GetIndexes()) != 1 {
		t.Errorf("ListIndexes: %v %v", list, err)
	}
	_, err = admin.ListIndexes(ctx, req(&adminpb.ListIndexesRequest{Parent: db + "/collectionGroups/-", PageSize: 1}, tok))
	wantErr(t, "a page size", err, connect.CodeInvalidArgument, "Invalid page size. Only 0 is supported.")
	_, err = admin.GetIndex(ctx, req(&adminpb.GetIndexRequest{Name: group + "/indexes/nonexistent"}, tok))
	wantErr(t, "a malformed id", err, connect.CodeInvalidArgument, `Invalid index resource id "nonexistent".`)
	got, err := ops.GetOperation(ctx, req(&longrunningpb.GetOperationRequest{Name: op.Msg.GetName()}, tok))
	if err != nil || !got.Msg.GetDone() || got.Msg.GetResponse() == nil {
		t.Errorf("GetOperation: %v %v", got, err)
	}
	all, err := ops.ListOperations(ctx, req(&longrunningpb.ListOperationsRequest{Name: db}, tok))
	if err != nil || len(all.Msg.GetOperations()) != 1 {
		t.Errorf("ListOperations: %v %v", all, err)
	}
	_, err = ops.GetOperation(ctx, req(&longrunningpb.GetOperationRequest{Name: db + "/operations/nonexistent"}, tok))
	wantErr(t, "a missing operation", err, connect.CodeNotFound, "Operation does not exist")
	if _, err := admin.DeleteIndex(ctx, req(&adminpb.DeleteIndexRequest{Name: md.GetIndex()}, tok)); err != nil {
		t.Errorf("DeleteIndex: %v", err)
	}
	_, err = admin.GetIndex(ctx, req(&adminpb.GetIndexRequest{Name: md.GetIndex()}, tok))
	wantErr(t, "a deleted index", err, connect.CodeNotFound, "index not found.")

	// A field: exempted, listed, read; a quoted path.
	fop, err := admin.UpdateField(ctx, req(&adminpb.UpdateFieldRequest{
		Field:      &adminpb.Field{Name: group + "/fields/`a b`", IndexConfig: &adminpb.Field_IndexConfig{}},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"index_config"}},
	}, tok))
	if err != nil || fop.Msg.GetDone() {
		t.Fatalf("UpdateField: %v %v", fop, err)
	}
	if err := x.env.Store.BuildIndexes(ctx, documents.IndexBuildOptions{}); err != nil {
		t.Fatal(err)
	}
	fields, err := admin.ListFields(ctx, req(&adminpb.ListFieldsRequest{Parent: group, Filter: "indexConfig.usesAncestorConfig:false"}, tok))
	if err != nil || len(fields.Msg.GetFields()) != 1 || fields.Msg.GetFields()[0].GetName() != group+"/fields/`a b`" {
		t.Errorf("ListFields: %v %v", fields, err)
	}
	_, err = admin.ListFields(ctx, req(&adminpb.ListFieldsRequest{Parent: group}, tok))
	wantErr(t, "no filter", err, connect.CodeInvalidArgument,
		"ListFieldsRequest.filter must include at least fields that do not use their ancestor configuration, or fields with TTLs.")
	f, err := admin.GetField(ctx, req(&adminpb.GetFieldRequest{Name: group + "/fields/__name__"}, tok))
	wantErr(t, "__name__", err, connect.CodeInvalidArgument, `Field path id "__name__" is invalid because it is reserved.`)
	f, err = admin.GetField(ctx, req(&adminpb.GetFieldRequest{Name: group + "/fields/v"}, tok))
	if err != nil || !f.Msg.GetIndexConfig().GetUsesAncestorConfig() {
		t.Errorf("GetField: %v %v", f, err)
	}
	bs, err := admin.ListBackupSchedules(ctx, req(&adminpb.ListBackupSchedulesRequest{Parent: db}, tok))
	if err != nil || len(bs.Msg.GetBackupSchedules()) != 0 {
		t.Errorf("ListBackupSchedules: %v %v", bs, err)
	}
	_, err = ops.CancelOperation(ctx, req(&longrunningpb.CancelOperationRequest{Name: op.Msg.GetName()}, tok))
	if connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Errorf("CancelOperation: %v", err)
	}
}

// The Admin API is an admin's, whatever dev_open_rules says.
func TestAdminGRPCCallers(t *testing.T) {
	x := newHarness(t, apitest.Options{DevOwnerToken: true, DevOpenRules: true}, docgrpc.Limits{})
	admin, _ := adminClients(x)
	ctx := t.Context()
	get := func(token string) error {
		_, err := admin.GetDatabase(ctx, req(&adminpb.GetDatabaseRequest{Name: db}, token))
		return err
	}
	for _, tok := range []string{x.env.JWT(t), x.env.OwnerPAT, "owner"} {
		if err := get(tok); err != nil {
			t.Errorf("admin %.12q: %v", tok, err)
		}
	}
	wantErr(t, "anonymous", get(""), connect.CodeUnauthenticated, "Request is missing required authentication credential. "+
		"Expected OAuth 2 access token, login cookie or other valid authentication credential. "+
		"See https://developers.google.com/identity/sign-in/web/devconsole-project.")
	wantErr(t, "end user", get(apitest.IDToken(t)), connect.CodePermissionDenied, "The caller does not have permission")
	_, err := admin.GetDatabase(ctx, req(&adminpb.GetDatabaseRequest{Name: other}, x.env.JWT(t)))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("another project: %v", err)
	}
}

// core's ErrIndexEntryTooLarge reaches gRPC with its google.rpc.ErrorInfo (f0-errors.json
// rest.name_bytes_6127; the same details assumed on gRPC).
func TestIndexEntryTooLargeGRPC(t *testing.T) {
	x := newHarness(t, apitest.Options{}, docgrpc.Limits{})
	var name strings.Builder
	for i := range 4 {
		if i > 0 {
			name.WriteString("/")
		}
		name.WriteString("c/" + strings.Repeat(string(rune('a'+i)), 1500))
	}
	name.WriteString("/c/")
	name.WriteString(strings.Repeat("e", 6127-name.Len()))
	_, err := x.client.Commit(t.Context(), req(&pb.CommitRequest{Database: db, Writes: []*pb.Write{{
		Operation: &pb.Write_Update{Update: &pb.Document{Name: root + "/" + name.String(), Fields: intField(1)}},
	}}}, x.env.JWT(t)))
	wantErr(t, "commit", err, connect.CodeInvalidArgument, "Index entry is too large.")
	ce, ok := errors.AsType[*connect.Error](err)
	if !ok || len(ce.Details()) != 1 {
		t.Fatalf("details: %v", err)
	}
	v, derr := ce.Details()[0].Value()
	if info, ok := v.(*errdetails.ErrorInfo); derr != nil || !ok || info.GetReason() != "INDEX_ENTRY_TOO_LARGE" || info.GetDomain() != "firestore.googleapis.com" {
		t.Errorf("detail: %v %v", v, derr)
	}
}
