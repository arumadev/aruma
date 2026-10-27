package grpc

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/arumadev/aruma/internal/services/documents/wire/pb/adminpb"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/adminpb/adminpbconnect"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/longrunningpb"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/longrunningpb/longrunningpbconnect"
)

// firestoreAdmin is google.firestore.admin.v1.FirestoreAdmin: the databases (read alone), the
// composite indexes and the fields, as package api serves them (ADR-025 §4.5). The other
// methods (backups, exports, user credentials, creating a database) are
// UnimplementedFirestoreAdminHandler's UNIMPLEMENTED.
type firestoreAdmin struct {
	adminpbconnect.UnimplementedFirestoreAdminHandler
	h *Handler
}

func (a firestoreAdmin) GetDatabase(ctx context.Context, req *connect.Request[adminpb.GetDatabaseRequest]) (*connect.Response[adminpb.Database], error) {
	return unary(ctx, req, a.h.svc.GetDatabase)
}

func (a firestoreAdmin) ListDatabases(ctx context.Context, req *connect.Request[adminpb.ListDatabasesRequest]) (*connect.Response[adminpb.ListDatabasesResponse], error) {
	return unary(ctx, req, a.h.svc.ListDatabases)
}

func (a firestoreAdmin) CreateIndex(ctx context.Context, req *connect.Request[adminpb.CreateIndexRequest]) (*connect.Response[longrunningpb.Operation], error) {
	return unary(ctx, req, a.h.svc.CreateIndex)
}

func (a firestoreAdmin) ListIndexes(ctx context.Context, req *connect.Request[adminpb.ListIndexesRequest]) (*connect.Response[adminpb.ListIndexesResponse], error) {
	return unary(ctx, req, a.h.svc.ListIndexes)
}

func (a firestoreAdmin) GetIndex(ctx context.Context, req *connect.Request[adminpb.GetIndexRequest]) (*connect.Response[adminpb.Index], error) {
	return unary(ctx, req, a.h.svc.GetIndex)
}

func (a firestoreAdmin) DeleteIndex(ctx context.Context, req *connect.Request[adminpb.DeleteIndexRequest]) (*connect.Response[emptypb.Empty], error) {
	return unary(ctx, req, a.h.svc.DeleteIndex)
}

func (a firestoreAdmin) GetField(ctx context.Context, req *connect.Request[adminpb.GetFieldRequest]) (*connect.Response[adminpb.Field], error) {
	return unary(ctx, req, a.h.svc.GetField)
}

func (a firestoreAdmin) ListFields(ctx context.Context, req *connect.Request[adminpb.ListFieldsRequest]) (*connect.Response[adminpb.ListFieldsResponse], error) {
	return unary(ctx, req, a.h.svc.ListFields)
}

func (a firestoreAdmin) UpdateField(ctx context.Context, req *connect.Request[adminpb.UpdateFieldRequest]) (*connect.Response[longrunningpb.Operation], error) {
	return unary(ctx, req, a.h.svc.UpdateField)
}

func (a firestoreAdmin) ListBackupSchedules(ctx context.Context, req *connect.Request[adminpb.ListBackupSchedulesRequest]) (*connect.Response[adminpb.ListBackupSchedulesResponse], error) {
	return unary(ctx, req, a.h.svc.ListBackupSchedules)
}

// operations is google.longrunning.Operations: GetOperation and ListOperations; cancelling and
// deleting an operation are UNIMPLEMENTED.
type operations struct {
	longrunningpbconnect.UnimplementedOperationsHandler
	h *Handler
}

func (o operations) GetOperation(ctx context.Context, req *connect.Request[longrunningpb.GetOperationRequest]) (*connect.Response[longrunningpb.Operation], error) {
	return unary(ctx, req, o.h.svc.GetOperation)
}

func (o operations) ListOperations(ctx context.Context, req *connect.Request[longrunningpb.ListOperationsRequest]) (*connect.Response[longrunningpb.ListOperationsResponse], error) {
	return unary(ctx, req, o.h.svc.ListOperations)
}
