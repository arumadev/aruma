package grpc

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/arumadev/aruma/internal/services/documents/api"
	"github.com/arumadev/aruma/internal/services/documents/wire"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// errOutboxFull ends a Listen stream whose client left its outbox full for stream_write_stall:
// it stopped reading (ADR-026 §3). Our text.
var errOutboxFull = wire.NewError(wire.CodeResourceExhausted,
	"The client is not reading the listen stream's responses. Reopen it with the targets' resume tokens.")

// Listen is the real-time stream of every client (ADR-026 §1): its first request within
// stream_first_message admits it, then package api answers its requests and the commits of its
// targets until either side ends it. A goroutine reads the client's requests while the stream
// sends, so a client that adds a target is answered while commits flow; and another sends what
// the stream queues in its outbox, so a slow client does not hold the stream's work. A Listen
// stream is bounded by its token and the stream limits (ADR-026 §3, §4), not by
// server_stream_max.
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
		release, err := h.watchListenStream(s, ls)
		if err != nil {
			return err
		}
		defer release()
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

		out := newOutbox(s, h.limits.StreamOutboxBytes, h.limits.StreamWriteStall)
		var sender sync.WaitGroup
		sender.Go(func() {
			// No write deadline: the outbox bounds a client that does not read.
			out.run(func(r *pb.ListenResponse) error { return s.sendWithin(0, func() error { return conn.Send(r) }) })
		})
		defer func() {
			out.stop()
			sender.Wait()
		}()
		err = ls.Serve(s.ctx, first, requests, out.push)
		// What was queued goes before the stream's status, unless the stream ended from outside.
		if s.ctx.Err() == nil {
			if ferr := out.flush(); ferr != nil && err == nil {
				err = ferr
			}
		}
		if errors.Is(err, errOutboxFull) {
			h.overflow(s, "stream_outbox_bytes")
			// The sender is stuck on the client: the stream's end cuts its write.
			s.cancel(errOutboxFull)
		}
		return err
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

// watchListenStream joins an admitted Listen stream to the handler's watch (watch.go): its count
// per project, its token's expiry and revocation, the lock's loss; and sets its targets and idle
// bounds. It returns what to call when the stream ends.
func (h *Handler) watchListenStream(s *serverStream, ls *api.ListenStream) (release func(), err error) {
	release, err = h.watch.join(s, ls.Admission())
	if err != nil {
		return nil, err
	}
	ls.Limit(api.ListenLimits{
		Targets:  h.limits.TargetsPerStream,
		Idle:     h.limits.StreamIdle,
		Overflow: func(limit string) { h.overflow(s, limit) },
	})
	return release, nil
}

// outbox is a Listen stream's queue of responses not yet sent, bounded by stream_outbox_bytes
// (ADR-026 §3). A push waits while it is full; a client that lets nothing out for
// stream_write_stall stopped reading, and its stream ends with RESOURCE_EXHAUSTED. A response
// alone is always taken: message_bytes bounds it below the outbox.
type outbox struct {
	s     *serverStream
	max   int64
	stall time.Duration

	mu    sync.Mutex
	queue []queued
	bytes int64 // queued and not yet sent, the response being sent included
	err   error // the sender's failure

	ready chan struct{} // tells the sender of a push
	room  chan struct{} // tells a waiting push or flush of a response sent
	done  chan struct{}
}

type queued struct {
	r *pb.ListenResponse
	n int64
}

func newOutbox(s *serverStream, limit int64, stall time.Duration) *outbox {
	return &outbox{s: s, max: limit, stall: stall,
		ready: make(chan struct{}, 1), room: make(chan struct{}, 1), done: make(chan struct{})}
}

func signal(c chan struct{}) {
	select {
	case c <- struct{}{}:
	default:
	}
}

// push queues r, waiting for room while the outbox is full.
func (o *outbox) push(r *pb.ListenResponse) error {
	n := int64(proto.Size(r))
	return o.wait(func() bool {
		if o.bytes > 0 && o.bytes+n > o.max {
			return false
		}
		o.queue = append(o.queue, queued{r: r, n: n})
		o.bytes += n
		signal(o.ready)
		return true
	})
}

// flush waits until every queued response is sent.
func (o *outbox) flush() error {
	return o.wait(func() bool { return o.bytes == 0 })
}

// wait runs try under o.mu until it succeeds, waiting for each response sent between tries;
// none sent for stall is errOutboxFull.
func (o *outbox) wait(try func() bool) error {
	var stalled *time.Timer
	defer func() {
		if stalled != nil {
			stalled.Stop()
		}
	}()
	for {
		o.mu.Lock()
		err, ok := o.err, false
		if err == nil {
			ok = try()
		}
		o.mu.Unlock()
		switch {
		case err != nil:
			return err
		case ok:
			return nil
		case stalled == nil:
			stalled = time.NewTimer(o.stall)
		}
		select {
		case <-o.room:
			stalled.Reset(o.stall)
		case <-stalled.C:
			return errOutboxFull
		case <-o.s.ctx.Done():
			return context.Cause(o.s.ctx)
		}
	}
}

// run sends the queued responses in order until stop, the stream's end, or a send fails, which
// ends the stream with its error.
func (o *outbox) run(send func(*pb.ListenResponse) error) {
	for {
		select {
		case <-o.ready:
		case <-o.done:
			return
		case <-o.s.ctx.Done():
			return
		}
		for {
			o.mu.Lock()
			if len(o.queue) == 0 {
				o.mu.Unlock()
				break
			}
			q := o.queue[0]
			o.queue[0] = queued{}
			o.queue = o.queue[1:]
			o.mu.Unlock()
			if err := send(q.r); err != nil {
				o.mu.Lock()
				o.err = err
				o.mu.Unlock()
				signal(o.room)
				o.s.cancel(err)
				return
			}
			o.mu.Lock()
			o.bytes -= q.n
			o.mu.Unlock()
			signal(o.room)
		}
	}
}

// stop ends the sender.
func (o *outbox) stop() { close(o.done) }
