package grpc

import (
	"context"
	"errors"
	"net/netip"
	"time"

	"connectrpc.com/connect"

	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/platform/httpx"
	"github.com/arumadev/aruma/internal/services/documents/api"
	"github.com/arumadev/aruma/internal/services/documents/wire"
)

// Peer is a stream that reaches the handler on another transport than gRPC's own: a WebChannel
// session (package webchannel). Its stream is the gRPC stream's in all but the transport: counted
// against streams_per_ip and streams_per_project, watched for its token and the lock's loss,
// ended by the drain, bounded by the same limits (ADR-026 §3-§5; ADR-027 §5.1).
type Peer struct {
	// Procedure names the stream in the route's log line and metrics.
	Procedure string
	Client    netip.Addr
	Transport authn.Transport
	// Deadlines are the stream's read and write deadlines on the transport's queues.
	Deadlines Deadlines
}

// ServeListen runs one Listen stream for p until it ends, as the gRPC route's Listen does, and
// returns its status: nil when the client ended it, otherwise a *wire.Error, REST's message
// included. ctx is the stream's life: its end ends the stream.
func (h *Handler) ServeListen(ctx context.Context, p Peer, c api.Call, conn ListenConn) (status *wire.Error) {
	start := time.Now()
	info := &rpc{transport: p.Transport, client: p.Client, rc: p.Deadlines}
	ctx = context.WithValue(ctx, rpcKey{}, info)
	// The first message is due within stream_first_message, as on gRPC (ADR-027 §3.2).
	_ = p.Deadlines.SetReadDeadline(start.Add(h.limits.StreamFirstMessage))
	defer func() {
		if r := recover(); r != nil {
			h.log.Error("panic", "request_id", httpx.RequestID(ctx), "procedure", p.Procedure, "panic", r)
			status = wire.Internal()
		}
		code := connect.Code(0)
		if status != nil {
			code = connect.Code(status.Code)
		}
		h.metrics.observe(p.Procedure, code, time.Since(start))
		h.log.Info("rpc",
			"request_id", httpx.RequestID(ctx),
			"procedure", p.Procedure,
			"code", codeName(code),
			"duration_ms", time.Since(start).Milliseconds(),
			"client_ip", p.Client.String(),
		)
	}()
	s, err := h.openServerStream(ctx, 0)
	if err != nil {
		return wire.From(err)
	}
	err = s.runTo(func() error { return h.listen(s, c, conn) }, s.status)
	if err == nil {
		return nil
	}
	if ce, ok := errors.AsType[*connect.Error](err); ok {
		// A Send that failed: the transport's own error, which its session already knows.
		return wire.NewError(wire.Code(ce.Code()), ce.Message())
	}
	return wire.From(err)
}
