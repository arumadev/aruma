package grpc

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"

	"github.com/arumadev/aruma/internal/services/documents/api"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// Listen is the real-time stream of every client (ADR-026 §1): its first request within
// stream_first_message admits it, then package api answers its requests and the commits of its
// targets until either side ends it. A goroutine reads the client's requests while the stream
// sends, so a client that adds a target is answered while commits flow. A Listen stream is
// bounded by its token and the stream limits (ADR-026 §3, §4), not by server_stream_max.
func (f firestore) Listen(ctx context.Context, conn *connect.BidiStream[pb.ListenRequest, pb.ListenResponse]) error {
	h := f.h
	s, err := h.openServerStream(ctx, 0)
	if err != nil {
		return connectError(err)
	}
	return s.run(func() error {
		defer s.cutReadsAtEnd()()

		first, err := receive(s, conn.Receive, errFirstMessage)
		if err != nil {
			return endOfRequests(err)
		}
		ls, err := h.svc.OpenListen(s.ctx, call(ctx, conn.RequestHeader()), first)
		if err != nil {
			return err
		}
		defer ls.Close()
		defer h.watchListenStream(s, ls)()
		s.admitted()

		requests := make(chan *pb.ListenRequest)
		done := make(chan struct{})
		var ending atomic.Bool
		var reader sync.WaitGroup
		reader.Go(func() {
			defer close(requests)
			for {
				req, err := conn.Receive()
				if err != nil {
					// A client that closed its side ends the stream normally (requests closes);
					// a failed read ends it with the read's error, unless the stream is ending
					// and cut the read itself.
					if !errors.Is(err, io.EOF) && !ending.Load() {
						s.cancel(err)
					}
					return
				}
				select {
				case requests <- req:
				case <-done:
					return
				}
			}
		})
		defer func() {
			ending.Store(true)
			close(done)
			_ = s.info.rc.SetReadDeadline(time.Now())
			reader.Wait()
		}()
		return ls.Serve(s.ctx, first, requests, func(r *pb.ListenResponse) error {
			return s.send(func() error { return conn.Send(r) })
		})
	})
}

// cutReadsAtEnd makes a stream ended from outside (the drain, the client's reset) stop waiting on
// a read. The returned stop waits for a cut in progress: the handler must not return while it
// touches the response.
func (s *serverStream) cutReadsAtEnd() (stop func()) {
	cut := make(chan struct{})
	after := context.AfterFunc(s.ctx, func() {
		defer close(cut)
		_ = s.info.rc.SetReadDeadline(time.Now())
	})
	return func() {
		if !after() {
			<-cut
		}
	}
}

// watchListenStream is where the shared track of stream admission (A3) joins a Listen stream once
// it is admitted: its count per project, its outbox and targets bounds, and its token's expiry
// and revocation, which end the stream through s. It returns what to call when the stream ends.
func (h *Handler) watchListenStream(*serverStream, *api.ListenStream) (release func()) {
	return func() {}
}
