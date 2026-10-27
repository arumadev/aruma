package grpc

import (
	"context"
	"errors"
	"io"
	"os"
	"time"

	"connectrpc.com/connect"

	"github.com/arumadev/aruma/internal/services/documents/api"
	"github.com/arumadev/aruma/internal/services/documents/wire"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

var (
	// errStreamIdle ends a Write stream that sent no request within stream_idle (ADR-026 §3).
	// The client that does not need the stream does not reopen it.
	errStreamIdle = wire.NewError(wire.CodeUnavailable, "The write stream was idle for too long. Reopen it when needed.")
	// errFirstMessage ends a stream whose first message did not arrive within
	// stream_first_message, before any work (ADR-026 §3).
	errFirstMessage = wire.From(context.DeadlineExceeded)
)

// Write is the mobile SDKs' write stream (ADR-026 §6): a handshake, then one atomic batch per
// request, answered in order. Each request is handled before the next is read, so the HTTP/2
// window bounds a client that pipelines.
func (f firestore) Write(ctx context.Context, conn *connect.BidiStream[pb.WriteRequest, pb.WriteResponse]) error {
	s, err := f.h.openServerStream(ctx, 0)
	if err != nil {
		return connectError(err)
	}
	return s.run(func() error { return f.h.write(s, call(ctx, conn.RequestHeader()), conn) })
}

// WriteConn is a Write stream's messages on its transport: gRPC's bidi stream, or a WebChannel
// session (ServeWrite), with ListenConn's contract.
type WriteConn interface {
	Receive() (*pb.WriteRequest, error)
	Send(*pb.WriteResponse) error
}

// write is a Write stream's work on s, for either transport.
func (h *Handler) write(s *serverStream, c api.Call, conn WriteConn) error {
	defer s.cutReadsAtEnd()()
	send := func(r *pb.WriteResponse) error { return s.send(func() error { return conn.Send(r) }) }

	// The handshake, within stream_first_message (its deadline is set by the transport).
	req, err := receive(s, conn.Receive, errFirstMessage)
	if err != nil {
		return endOfRequests(err)
	}
	ws, res, err := h.svc.OpenWriteStream(s.ctx, c, req)
	if err != nil {
		return err
	}
	release, err := h.watchWriteStream(s, ws)
	if err != nil {
		return err
	}
	defer release()
	s.admitted()
	if err := send(res); err != nil {
		return err
	}
	for {
		_ = s.info.rc.SetReadDeadline(time.Now().Add(h.limits.StreamIdle))
		req, err := receive(s, conn.Receive, errStreamIdle)
		if err != nil {
			return endOfRequests(err)
		}
		res, err := ws.Write(s.ctx, req)
		if err != nil {
			return err
		}
		if err := send(res); err != nil {
			return err
		}
	}
}

// watchWriteStream joins an admitted Write stream to the handler's watch (watch.go): its count
// per project, its token's expiry and revocation, and the lock's loss, which end the stream
// through s. It returns what to call when the stream ends.
func (h *Handler) watchWriteStream(s *serverStream, ws *api.WriteStream) (release func(), err error) {
	return h.watch.join(s, ws.Admission())
}

// receive reads a stream's next request; a read that ran into its deadline is timeout.
func receive[T any](s *serverStream, recv func() (*T, error), timeout error) (*T, error) {
	req, err := recv()
	switch {
	case err == nil:
		return req, nil
	case s.ctx.Err() != nil:
		return nil, err // ended from outside: finish gives its status
	case errors.Is(err, io.EOF):
		return nil, io.EOF
	case errors.Is(err, os.ErrDeadlineExceeded), connect.CodeOf(err) == connect.CodeDeadlineExceeded:
		return nil, timeout
	}
	return nil, err
}

// endOfRequests: a client that closed its side ends the stream normally.
func endOfRequests(err error) error {
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}
