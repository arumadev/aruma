package grpc

import (
	"context"
	"net/http"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/arumadev/aruma/internal/services/documents/api"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb/firestorepbconnect"
)

// firestore is google.firestore.v1.Firestore. The F1 and F2 methods call package api; the others are
// UnimplementedFirestoreHandler's UNIMPLEMENTED until their phase.
type firestore struct {
	firestorepbconnect.UnimplementedFirestoreHandler
	h *Handler
}

// call is how a request arrived, for package api: its credential, its transport as the route
// saw it, and google-cloud-resource-prefix.
func call(ctx context.Context, header http.Header) api.Call {
	c := api.Call{
		Authorization:  header.Get("Authorization"),
		ResourcePrefix: header.Get("Google-Cloud-Resource-Prefix"),
	}
	if info := rpcOf(ctx); info != nil {
		c.Transport = info.transport
	}
	return c
}

// unary runs a unary method of package api.
func unary[Req, Res any](ctx context.Context, req *connect.Request[Req], f func(context.Context, api.Call, *Req) (*Res, error)) (*connect.Response[Res], error) {
	res, err := f(ctx, call(ctx, req.Header()), req.Msg)
	if err != nil {
		return nil, connectError(err)
	}
	return connect.NewResponse(res), nil
}

func (f firestore) GetDocument(ctx context.Context, req *connect.Request[pb.GetDocumentRequest]) (*connect.Response[pb.Document], error) {
	return unary(ctx, req, f.h.svc.GetDocument)
}

func (f firestore) ListDocuments(ctx context.Context, req *connect.Request[pb.ListDocumentsRequest]) (*connect.Response[pb.ListDocumentsResponse], error) {
	return unary(ctx, req, f.h.svc.ListDocuments)
}

func (f firestore) CreateDocument(ctx context.Context, req *connect.Request[pb.CreateDocumentRequest]) (*connect.Response[pb.Document], error) {
	return unary(ctx, req, f.h.svc.CreateDocument)
}

func (f firestore) UpdateDocument(ctx context.Context, req *connect.Request[pb.UpdateDocumentRequest]) (*connect.Response[pb.Document], error) {
	return unary(ctx, req, f.h.svc.UpdateDocument)
}

func (f firestore) DeleteDocument(ctx context.Context, req *connect.Request[pb.DeleteDocumentRequest]) (*connect.Response[emptypb.Empty], error) {
	return unary(ctx, req, f.h.svc.DeleteDocument)
}

func (f firestore) ListCollectionIds(ctx context.Context, req *connect.Request[pb.ListCollectionIdsRequest]) (*connect.Response[pb.ListCollectionIdsResponse], error) {
	return unary(ctx, req, f.h.svc.ListCollectionIds)
}

func (f firestore) Commit(ctx context.Context, req *connect.Request[pb.CommitRequest]) (*connect.Response[pb.CommitResponse], error) {
	return unary(ctx, req, f.h.svc.Commit)
}

func (f firestore) BatchWrite(ctx context.Context, req *connect.Request[pb.BatchWriteRequest]) (*connect.Response[pb.BatchWriteResponse], error) {
	return unary(ctx, req, f.h.svc.BatchWrite)
}

func (f firestore) BeginTransaction(ctx context.Context, req *connect.Request[pb.BeginTransactionRequest]) (*connect.Response[pb.BeginTransactionResponse], error) {
	return unary(ctx, req, f.h.svc.BeginTransaction)
}

func (f firestore) Rollback(ctx context.Context, req *connect.Request[pb.RollbackRequest]) (*connect.Response[emptypb.Empty], error) {
	return unary(ctx, req, f.h.svc.Rollback)
}

// BatchGetDocuments is a server stream: one response per document asked, in order, after the
// transaction's id when the request asks for a new one.
func (f firestore) BatchGetDocuments(ctx context.Context, req *connect.Request[pb.BatchGetDocumentsRequest], stream *connect.ServerStream[pb.BatchGetDocumentsResponse]) error {
	s, err := f.h.serverStream(ctx)
	if err != nil {
		return connectError(err)
	}
	return s.run(func() error {
		admitted := false
		return f.h.svc.BatchGetDocuments(s.ctx, call(ctx, req.Header()), req.Msg, func(r *pb.BatchGetDocumentsResponse) error {
			if !admitted {
				s.admitted()
				admitted = true
			}
			return s.send(func() error { return stream.Send(r) })
		})
	})
}
