package apitest

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb/firestorepbconnect"
)

// ListenWait bounds every wait for a Listen response: CI machines are slow.
const ListenWait = 5 * time.Second

// ListenClient is one Listen stream of a test, each frame checked by its ListenChecker. A
// goroutine reads the responses, so a test waits for them with a bound.
type ListenClient struct {
	t       testing.TB
	st      *connect.BidiStreamForClient[pb.ListenRequest, pb.ListenResponse]
	Checker *ListenChecker
	frames  chan *pb.ListenResponse
	gate    atomic.Pointer[chan struct{}]
	// End is the stream's final error once frames closes: nil when it ended OK.
	End error
}

// OpenListen opens a Listen stream with token as its credential and header's pairs as its
// metadata; Database is sent on every request that has none.
func OpenListen(t testing.TB, c firestorepbconnect.FirestoreClient, token string, header ...string) *ListenClient {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	st := c.Listen(ctx)
	if token != "" {
		st.RequestHeader().Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(header); i += 2 {
		st.RequestHeader().Set(header[i], header[i+1])
	}
	l := &ListenClient{t: t, st: st, Checker: NewListenChecker(t), frames: make(chan *pb.ListenResponse, 1024)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(l.frames)
		for {
			if g := l.gate.Load(); g != nil {
				<-*g
			}
			res, err := st.Receive()
			if err != nil {
				if !errors.Is(err, io.EOF) {
					l.End = err
				}
				return
			}
			l.frames <- res
		}
	}()
	t.Cleanup(func() {
		l.Release()
		_ = st.CloseRequest()
		cancel()
		<-done
	})
	return l
}

// Send sends a request; its database is the test project's default unless set.
func (l *ListenClient) Send(req *pb.ListenRequest) {
	l.t.Helper()
	if req.Database == "" {
		req.Database = "projects/" + Project + "/databases/(default)"
	}
	l.Checker.Sent(req)
	if err := l.st.Send(req); err != nil && !errors.Is(err, io.EOF) {
		l.t.Fatalf("listen send: %v", err)
	}
}

// Add sends an add_target.
func (l *ListenClient) Add(t *pb.Target) {
	l.t.Helper()
	l.Send(&pb.ListenRequest{TargetChange: &pb.ListenRequest_AddTarget{AddTarget: t}})
}

// Remove sends a remove_target.
func (l *ListenClient) Remove(id int32) {
	l.t.Helper()
	l.Send(&pb.ListenRequest{TargetChange: &pb.ListenRequest_RemoveTarget{RemoveTarget: id}})
}

// Hold stops reading the stream after the response being read, so the server's sends fill the
// flow-control window and block; Release reads on.
func (l *ListenClient) Hold() {
	ch := make(chan struct{})
	l.gate.Store(&ch)
}

// Release ends a Hold.
func (l *ListenClient) Release() {
	if g := l.gate.Swap(nil); g != nil {
		close(*g)
	}
}

// CloseSend ends the client's side.
func (l *ListenClient) CloseSend() { _ = l.st.CloseRequest() }

// Next returns the next response, checked, or nil once the stream ended (End tells how) or wait
// passed.
func (l *ListenClient) Next(wait time.Duration) *pb.ListenResponse {
	l.t.Helper()
	select {
	case res, ok := <-l.frames:
		if !ok {
			return nil
		}
		l.Checker.Received(res)
		return res
	case <-time.After(wait):
		return nil
	}
}

// Until returns the responses up to the first that stop accepts, which it includes; it fails the
// test when the stream ends or ListenWait passes first.
func (l *ListenClient) Until(stop func(*pb.ListenResponse) bool) []*pb.ListenResponse {
	l.t.Helper()
	var out []*pb.ListenResponse
	for {
		res := l.Next(ListenWait)
		if res == nil {
			l.t.Fatalf("listen: no response after %d (end %v)", len(out), l.End)
		}
		out = append(out, res)
		if stop(res) {
			return out
		}
	}
}

// UntilGlobal returns the responses up to the next global snapshot.
func (l *ListenClient) UntilGlobal() []*pb.ListenResponse {
	l.t.Helper()
	return l.Until(IsGlobal)
}

// Quiet fails the test when a response comes within d.
func (l *ListenClient) Quiet(d time.Duration) {
	l.t.Helper()
	if res := l.Next(d); res != nil {
		l.t.Errorf("listen: unexpected %v", res)
	}
}

// Ended waits for the stream's end and returns its error: nil when it ended OK.
func (l *ListenClient) Ended() error {
	l.t.Helper()
	deadline := time.After(ListenWait)
	for {
		select {
		case res, ok := <-l.frames:
			if !ok {
				return l.End
			}
			l.Checker.Received(res)
		case <-deadline:
			l.t.Fatal("listen: the stream did not end")
		}
	}
}

// IsGlobal reports a global snapshot: NO_CHANGE without target ids.
func IsGlobal(r *pb.ListenResponse) bool {
	tc := r.GetTargetChange()
	return tc != nil && tc.GetTargetChangeType() == pb.TargetChange_NO_CHANGE && len(tc.GetTargetIds()) == 0
}

// IsTargetChange reports a target change of kind for id.
func IsTargetChange(r *pb.ListenResponse, kind pb.TargetChange_TargetChangeType, id int32) bool {
	tc := r.GetTargetChange()
	return tc != nil && tc.GetTargetChangeType() == kind && len(tc.GetTargetIds()) == 1 && tc.GetTargetIds()[0] == id
}
