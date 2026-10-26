package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb/firestorepbconnect"
)

// maxMessage is what the mobile clients accept (17 MiB: a 16 MiB document and overhead); the
// server takes as much.
const maxMessage = 17 << 20

// indexErrorDoc is a document name that makes Commit fail as a query without its composite index
// does: FAILED_PRECONDITION, the console link in the message, and the same link in a
// google.rpc.Help detail, so the clients' handling of status details can be read.
const indexErrorDoc = "/documents/spike/index-error"

const indexLink = "https://console.firebase.google.com/v1/r/project/spike-project/firestore/indexes?create_composite=spike"

func indexError(req *pb.CommitRequest) bool {
	return slices.ContainsFunc(req.GetWrites(), func(w *pb.Write) bool {
		return strings.HasSuffix(w.GetUpdate().GetName(), indexErrorDoc)
	})
}

var indexHelp = &errdetails.Help{Links: []*errdetails.Help_Link{{Description: "Create the composite index", Url: indexLink}}}

const indexMessage = "The query requires an index. You can create it here: " + indexLink

// newGRPC is option (b): grpc-go's Server, served through its ServeHTTP on our own listener.
func newGRPC(f *fake, log *slog.Logger) *grpc.Server {
	s := grpc.NewServer(
		grpc.MaxRecvMsgSize(maxMessage),
		grpc.ChainUnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
			res, err := h(ctx, req)
			log.Info("rpc", "stack", "grpcgo", "method", info.FullMethod, "code", status.Code(err).String())
			return res, err
		}),
		grpc.ChainStreamInterceptor(func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, h grpc.StreamHandler) error {
			err := h(srv, ss)
			log.Info("rpc", "stack", "grpcgo", "method", info.FullMethod, "code", status.Code(err).String())
			return err
		}),
	)
	pb.RegisterFirestoreServer(s, grpcFirestore{f: f})
	return s
}

type grpcFirestore struct {
	pb.UnimplementedFirestoreServer
	f *fake
}

func (s grpcFirestore) Commit(_ context.Context, req *pb.CommitRequest) (*pb.CommitResponse, error) {
	if indexError(req) {
		st, err := status.New(codes.FailedPrecondition, indexMessage).WithDetails(indexHelp)
		if err != nil {
			return nil, err
		}
		return nil, st.Err()
	}
	return s.f.commit(req), nil
}

func (s grpcFirestore) BatchGetDocuments(req *pb.BatchGetDocumentsRequest, stream grpc.ServerStreamingServer[pb.BatchGetDocumentsResponse]) error {
	return s.f.batchGet(req, stream.Send)
}

func (s grpcFirestore) Listen(stream grpc.BidiStreamingServer[pb.ListenRequest, pb.ListenResponse]) error {
	return grpcStatus(s.f.listen(stream))
}

func (s grpcFirestore) Write(stream grpc.BidiStreamingServer[pb.WriteRequest, pb.WriteResponse]) error {
	return grpcStatus(s.f.write(stream))
}

func grpcStatus(err error) error {
	if errors.Is(err, errDraining) {
		return status.Error(codes.Unavailable, err.Error())
	}
	return err
}

// newConnect is option (a): connect-go's handlers, which speak gRPC (and gRPC-Web and Connect)
// on any net/http server.
func newConnect(f *fake, log *slog.Logger) http.Handler {
	logCode := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			res, err := next(ctx, req)
			log.Info("rpc", "stack", "connect", "method", req.Spec().Procedure, "code", codeOf(err))
			return res, err
		}
	})
	_, h := firestorepbconnect.NewFirestoreHandler(connectFirestore{f: f},
		connect.WithReadMaxBytes(maxMessage),
		connect.WithInterceptors(logCode, streamLogger{log}),
	)
	return h
}

func codeOf(err error) string {
	if err == nil {
		return "OK"
	}
	return connect.CodeOf(err).String()
}

// streamLogger is connect's streaming interceptor: an interface with three methods where
// grpc-go takes one function.
type streamLogger struct{ log *slog.Logger }

func (l streamLogger) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc { return next }

func (l streamLogger) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (l streamLogger) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		err := next(ctx, conn)
		l.log.Info("rpc", "stack", "connect", "method", conn.Spec().Procedure, "code", codeOf(err))
		return err
	}
}

type connectFirestore struct {
	firestorepbconnect.UnimplementedFirestoreHandler
	f *fake
}

func (s connectFirestore) Commit(_ context.Context, req *connect.Request[pb.CommitRequest]) (*connect.Response[pb.CommitResponse], error) {
	if indexError(req.Msg) {
		err := connect.NewError(connect.CodeFailedPrecondition, errors.New(indexMessage))
		d, derr := connect.NewErrorDetail(indexHelp)
		if derr != nil {
			return nil, derr
		}
		err.AddDetail(d)
		return nil, err
	}
	return connect.NewResponse(s.f.commit(req.Msg)), nil
}

func (s connectFirestore) BatchGetDocuments(_ context.Context, req *connect.Request[pb.BatchGetDocumentsRequest], stream *connect.ServerStream[pb.BatchGetDocumentsResponse]) error {
	return s.f.batchGet(req.Msg, stream.Send)
}

func (s connectFirestore) Listen(ctx context.Context, stream *connect.BidiStream[pb.ListenRequest, pb.ListenResponse]) error {
	return connectStatus(s.f.listen(connectBidi[pb.ListenRequest, pb.ListenResponse]{ctx, stream}))
}

func (s connectFirestore) Write(ctx context.Context, stream *connect.BidiStream[pb.WriteRequest, pb.WriteResponse]) error {
	return connectStatus(s.f.write(connectBidi[pb.WriteRequest, pb.WriteResponse]{ctx, stream}))
}

func connectStatus(err error) error {
	if errors.Is(err, errDraining) {
		return connect.NewError(connect.CodeUnavailable, err)
	}
	return err
}

// connectBidi gives connect's stream the shape of grpc-go's, which the fake takes.
type connectBidi[Req, Res any] struct {
	ctx context.Context
	s   *connect.BidiStream[Req, Res]
}

func (b connectBidi[Req, Res]) Context() context.Context { return b.ctx }
func (b connectBidi[Req, Res]) Recv() (*Req, error)      { return b.s.Receive() }
func (b connectBidi[Req, Res]) Send(m *Res) error        { return b.s.Send(m) }
