// Package grpc is the Document Database's gRPC surface (ADR-027): connect-go handlers for
// google.firestore.v1.Firestore, google.firestore.admin.v1.FirestoreAdmin and
// google.longrunning.Operations, on Aruma's one public port. internal/server sends it every
// request whose content-type is application/grpc, before httpx.Common, whose writer hides the
// Flush the streams need; so this route has its own middleware (ADR-027 §3.1):
//
//   - the request id and the client address, as Common sets them (httpx.Begin);
//   - no body cap: connect.WithReadMaxBytes bounds each message instead;
//   - panics recovered as INTERNAL in gRPC form;
//   - one log line and the documents_rpc_* metrics per RPC, with its code;
//   - the stream deadlines (ADR-027 §3.2): a stream must deliver its first message within
//     stream_first_message; a server stream then writes each message within stream_write_stall,
//     and lasts at most its grpc-timeout or server_stream_max;
//   - the drain (ADR-027 §3.4): Shutdown answers new streams UNAVAILABLE and ends the open ones
//     with UNAVAILABLE, which every client retries.
//
// Every method that is not served yet, and every unknown one, answers UNIMPLEMENTED in gRPC
// form. The methods themselves are package api's.
package grpc

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/platform/httpx"
	"github.com/arumadev/aruma/internal/platform/idtoken"
	"github.com/arumadev/aruma/internal/services/documents/api"
	"github.com/arumadev/aruma/internal/services/documents/wire"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/adminpb"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/adminpb/adminpbconnect"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb/firestorepbconnect"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/longrunningpb"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/longrunningpb/longrunningpbconnect"
)

// Limits are the route's limits (config documents.limits).
type Limits struct {
	// MessageBytes caps one message, in either direction's read (17 MiB by default).
	MessageBytes int64
	// StreamFirstMessage bounds the wait for a stream's first message (10 s).
	StreamFirstMessage time.Duration
	// StreamIdle ends a Listen stream without a target, or a Write stream, that sent no request
	// for this long (5 min).
	StreamIdle time.Duration
	// StreamWriteStall bounds each message a server stream writes (30 s).
	StreamWriteStall time.Duration
	// ServerStreamMax bounds a server stream without grpc-timeout (5 min).
	ServerStreamMax time.Duration
	// StreamsPerIP caps the streams open from one client address (500).
	StreamsPerIP int
	// StreamsPerProject caps the Listen and Write streams open on one project (10000).
	StreamsPerProject int
	// StreamOutboxBytes caps what waits to be sent on one Listen stream (64 MiB).
	StreamOutboxBytes int64
	// TargetsPerStream caps one Listen stream's targets (1000).
	TargetsPerStream int
}

// Options configure the handler.
type Options struct {
	Service *api.Service
	// TrustedProxies is trusted_proxies, for the client address.
	TrustedProxies []netip.Prefix
	Limits         Limits
	// Metrics, when set, counts every RPC.
	Metrics *Metrics
	// IDTokens verifies an end user's ID token when its stream opens, and again when
	// revocations were lost; Revocations tells of the users whose tokens stop being valid
	// (ADR-026 §4). Without them an end user's stream is watched for neither.
	IDTokens    idtoken.Verifier
	Revocations idtoken.Revocations
	Logger      *slog.Logger
}

// Handler serves the Document Database's gRPC methods. It is safe for concurrent use.
type Handler struct {
	svc     *api.Service
	trusted []netip.Prefix
	limits  Limits
	metrics *Metrics
	log     *slog.Logger
	mux     *http.ServeMux

	drain     context.Context
	stopDrain context.CancelFunc
	draining  atomic.Bool

	mu      sync.Mutex
	streams map[netip.Addr]int
	open    int

	// watch holds the admitted Listen and Write streams (watch.go).
	watch streamWatch
}

// New returns the handler.
func New(o Options) *Handler {
	l := o.Limits
	l.MessageBytes = cmp.Or(l.MessageBytes, 17<<20)
	l.StreamFirstMessage = cmp.Or(l.StreamFirstMessage, 10*time.Second)
	l.StreamIdle = cmp.Or(l.StreamIdle, 5*time.Minute)
	l.StreamWriteStall = cmp.Or(l.StreamWriteStall, 30*time.Second)
	l.ServerStreamMax = cmp.Or(l.ServerStreamMax, 5*time.Minute)
	l.StreamsPerIP = cmp.Or(l.StreamsPerIP, 500)
	l.StreamsPerProject = cmp.Or(l.StreamsPerProject, 10_000)
	l.StreamOutboxBytes = cmp.Or(l.StreamOutboxBytes, 64<<20)
	l.TargetsPerStream = cmp.Or(l.TargetsPerStream, 1000)
	h := &Handler{
		svc: o.Service, trusted: o.TrustedProxies, limits: l, metrics: o.Metrics,
		log: cmp.Or(o.Logger, slog.Default()), streams: make(map[netip.Addr]int),
	}
	h.watch.init(h, o.IDTokens, o.Revocations)
	h.drain, h.stopDrain = context.WithCancel(context.Background())
	opts := []connect.HandlerOption{
		connect.WithReadMaxBytes(int(l.MessageBytes)),
		connect.WithRecover(h.recovered),
		connect.WithInterceptors(outcome{}),
	}
	h.mux = http.NewServeMux()
	h.mux.Handle(firestorepbconnect.NewFirestoreHandler(firestore{h: h}, opts...))
	h.mux.Handle(adminpbconnect.NewFirestoreAdminHandler(firestoreAdmin{h: h}, opts...))
	h.mux.Handle(longrunningpbconnect.NewOperationsHandler(operations{h: h}, opts...))
	return h
}

// IsGRPC reports whether a request speaks gRPC: a content-type of application/grpc, or
// application/grpc+proto and the like (ADR-027 §2.1). gRPC-Web is not: no client of the
// Document Database sends it.
func IsGRPC(r *http.Request) bool {
	rest, ok := strings.CutPrefix(r.Header.Get("Content-Type"), "application/grpc")
	return ok && (rest == "" || rest[0] == '+' || rest[0] == ';')
}

// procedures are the methods of the three services the route serves, as their paths: any
// other path is answered UNIMPLEMENTED here, since connect's own answer would be a 404 page.
var procedures = func() map[string]bool {
	out := make(map[string]bool)
	for _, f := range []protoreflect.FileDescriptor{
		firestorepb.File_google_firestore_v1_firestore_proto,
		adminpb.File_google_firestore_admin_v1_firestore_admin_proto,
		longrunningpb.File_google_longrunning_operations_proto,
	} {
		for i := range f.Services().Len() {
			sd := f.Services().Get(i)
			for j := range sd.Methods().Len() {
				out["/"+string(sd.FullName())+"/"+string(sd.Methods().Get(j).Name())] = true
			}
		}
	}
	return out
}()

// streamProcedures are the methods whose request is a stream, or whose answer is (ADR-027
// §3.2): the first-message deadline applies to them, not to unary calls.
var streamProcedures = map[string]bool{
	firestorepbconnect.FirestoreBatchGetDocumentsProcedure:   true,
	firestorepbconnect.FirestoreRunQueryProcedure:            true,
	firestorepbconnect.FirestoreRunAggregationQueryProcedure: true,
	firestorepbconnect.FirestoreExecutePipelineProcedure:     true,
	firestorepbconnect.FirestoreListenProcedure:              true,
	firestorepbconnect.FirestoreWriteProcedure:               true,
}

// rpc is what the route knows of one RPC, shared with the handlers through the context.
type rpc struct {
	transport authn.Transport
	client    netip.Addr
	rc        *http.ResponseController
	code      atomic.Int32 // the outcome, set by the interceptor
}

type rpcKey struct{}

func rpcOf(ctx context.Context) *rpc {
	r, _ := ctx.Value(rpcKey{}).(*rpc)
	return r
}

// ServeHTTP is the route's middleware (package doc), then connect.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	r, id, ip := httpx.Begin(w, r, h.trusted)
	info := &rpc{transport: authn.TransportOf(r), client: ip, rc: http.NewResponseController(w)}
	info.code.Store(-1)
	procedure := r.URL.Path
	if streamProcedures[procedure] {
		// The server's ReadTimeout stays until the first message is in and admitted; this
		// shortens it to stream_first_message (ADR-027 §3.2).
		if err := info.rc.SetReadDeadline(start.Add(h.limits.StreamFirstMessage)); err != nil {
			h.log.Warn("documents: cannot set the stream's read deadline", "error", err)
		}
	}
	r = r.WithContext(context.WithValue(r.Context(), rpcKey{}, info))
	if !procedures[procedure] {
		writeStatus(w, connect.CodeUnimplemented, "Method not found: "+strings.TrimPrefix(procedure, "/"))
		info.code.Store(int32(connect.CodeUnimplemented))
	} else {
		h.mux.ServeHTTP(w, r)
		genericUTF8Error(w.Header())
	}
	code := connect.Code(max(info.code.Load(), 0))
	h.metrics.observe(procedure, code, time.Since(start))
	h.log.Info("rpc",
		"request_id", id,
		"procedure", truncate(procedure, 128),
		"code", codeName(code),
		"duration_ms", time.Since(start).Milliseconds(),
		"client_ip", ip.String(),
	)
}

// genericUTF8Error gives a request that did not unmarshal because a string in it is not UTF-8
// production's generic answer, with no details (f2-non-utf8.json grpc.*): connect words it after
// the proto decoder. The trailers are in the header map until the handler returns.
func genericUTF8Error(h http.Header) {
	for k, vs := range h {
		if len(vs) == 1 && strings.EqualFold(k, http.TrailerPrefix+"Grpc-Message") &&
			strings.HasPrefix(vs[0], "unmarshal message: ") && strings.HasSuffix(vs[0], ": string field contains invalid UTF-8") {
			vs[0] = "Request contains an invalid argument."
		}
	}
}

// recovered answers a panic as INTERNAL, logged with its stack.
func (h *Handler) recovered(ctx context.Context, spec connect.Spec, _ http.Header, p any) error {
	h.log.Error("panic", "request_id", httpx.RequestID(ctx), "procedure", spec.Procedure, "panic", p)
	return wire.Internal().Connect()
}

// Shutdown drains the route (ADR-027 §3.4): new streams are answered UNAVAILABLE, and the open
// ones end with UNAVAILABLE at once. Unary calls in progress finish under http.Server.Shutdown.
// It returns when the open streams have ended, or ctx has.
func (h *Handler) Shutdown(ctx context.Context) error {
	h.draining.Store(true)
	h.stopDrain()
	t := time.NewTicker(5 * time.Millisecond)
	defer t.Stop()
	for h.OpenStreams() > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
	return nil
}

// OpenStreams is the number of streams open now.
func (h *Handler) OpenStreams() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.open
}

// Errors of the route itself; the texts are ours.
var (
	errDraining = wire.NewError(wire.CodeUnavailable,
		"The server is shutting down. Retry the request.")
	errTooManyStreams = wire.NewError(wire.CodeResourceExhausted,
		"Too many streams are open from this address.")
	errStreamMax = errors.New("the stream reached server_stream_max")
)

// openStream counts a stream from client against streams_per_ip, or refuses it; release ends
// the count.
func (h *Handler) openStream(client netip.Addr) (release func(), err error) {
	if h.draining.Load() {
		return nil, errDraining
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.streams[client] >= h.limits.StreamsPerIP {
		return nil, errTooManyStreams
	}
	h.streams[client]++
	h.open++
	return sync.OnceFunc(func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.open--
		if h.streams[client]--; h.streams[client] <= 0 {
			delete(h.streams, client)
		}
	}), nil
}

// serverStream runs a server stream's lifetime around its work (ADR-027 §3.2, §3.4): counted
// against streams_per_ip; bounded by grpc-timeout (connect's deadline) and server_stream_max,
// whichever is sooner; ended by the drain. However it ends, a write stuck on a client that does
// not read is cut too. run maps the stream's error to its status.
type serverStream struct {
	ctx     context.Context
	info    *rpc
	stall   time.Duration
	release func()
	cancel  context.CancelCauseFunc
	stops   []func() bool

	mu      sync.Mutex // orders the cut of a stuck write with the sends and the trailers
	sending bool       // a Send is in progress: only then is it cut, which resets the stream
}

func (h *Handler) serverStream(ctx context.Context) (*serverStream, error) {
	return h.openServerStream(ctx, h.limits.ServerStreamMax)
}

// openServerStream is serverStream with the stream's own bound: a Write stream is bounded by
// stream_idle and its token (ADR-026 §3, §4), not by server_stream_max, so it passes none.
func (h *Handler) openServerStream(ctx context.Context, maxLife time.Duration) (*serverStream, error) {
	info := rpcOf(ctx)
	release, err := h.openStream(info.client)
	if errors.Is(err, errTooManyStreams) {
		h.overflowed(ctx, info, "streams_per_ip")
	}
	if err != nil {
		return nil, err
	}
	s := &serverStream{info: info, stall: h.limits.StreamWriteStall}
	// server_stream_max bounds a grpc-timeout too: a client cannot hold a stream for hours.
	cancel := context.CancelFunc(func() {})
	if maxLife > 0 {
		ctx, cancel = context.WithTimeoutCause(ctx, maxLife, errStreamMax)
	}
	s.release = func() { cancel(); release() }
	s.ctx, s.cancel = context.WithCancelCause(ctx)
	s.stops = []func() bool{
		context.AfterFunc(h.drain, func() { s.cancel(errDraining) }),
		// A Send stuck on flow control returns once its write deadline passes. That resets the
		// stream, so it is done only to a Send in progress: otherwise the handler sees the end of
		// its context and answers with a status.
		context.AfterFunc(s.ctx, func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.sending {
				_ = info.rc.SetWriteDeadline(time.Now())
			}
		}),
	}
	return s, nil
}

// admitted lifts the first-message deadline: the request is in and its caller admitted.
func (s *serverStream) admitted() {
	_ = s.info.rc.SetReadDeadline(time.Time{})
}

// send writes one message within stream_write_stall.
func (s *serverStream) send(send func() error) error {
	return s.sendWithin(s.stall, send)
}

// sendWithin writes one message within d; 0 is no bound but the stream's end, which cuts it.
func (s *serverStream) sendWithin(d time.Duration, send func() error) error {
	s.mu.Lock()
	if s.ctx.Err() != nil {
		s.mu.Unlock()
		return context.Cause(s.ctx)
	}
	s.sending = true
	deadline := time.Time{}
	if d > 0 {
		deadline = time.Now().Add(d)
	}
	_ = s.info.rc.SetWriteDeadline(deadline)
	s.mu.Unlock()
	err := send()
	s.mu.Lock()
	s.sending = false
	s.mu.Unlock()
	return err
}

// run runs the stream's work and ends the stream, even when the work panics: the panic goes on
// to connect's recovery, the stream's count and its context's callbacks released.
func (s *serverStream) run(work func() error) (err error) {
	done := false
	defer func() {
		if !done {
			s.finish(nil)
		}
	}()
	err = work()
	done = true
	return s.finish(err)
}

// finish ends the stream and returns its status for err: the drain's UNAVAILABLE and the
// deadline's DEADLINE_EXCEEDED take precedence over the error they caused.
func (s *serverStream) finish(err error) error {
	for _, stop := range s.stops {
		stop()
	}
	defer s.release()
	ended := s.ctx.Err() != nil
	cause := context.Cause(s.ctx)
	s.cancel(nil)
	// The trailers are a write too: within the stall bound, like every message.
	_ = s.info.rc.SetWriteDeadline(time.Now().Add(s.stall))
	if ended {
		switch {
		case errors.Is(cause, errDraining):
			return errDraining.Connect()
		case errors.Is(cause, errStreamMax), errors.Is(cause, context.DeadlineExceeded):
			return wire.From(context.DeadlineExceeded).Connect()
		}
		// A status the stream was closed with from outside: its token, a limit, the lock's loss.
		if we, ok := errors.AsType[*wire.Error](cause); ok {
			return we.Connect()
		}
	}
	if err == nil {
		return nil
	}
	return connectError(err)
}

// connectError renders any error in gRPC form; a *connect.Error (a Send that failed) passes.
func connectError(err error) error {
	if ce, ok := errors.AsType[*connect.Error](err); ok {
		return ce
	}
	return wire.From(err).Connect()
}

// outcome records each RPC's code for the route's log line and metrics.
type outcome struct{}

func (outcome) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		res, err := next(ctx, req)
		record(ctx, err)
		return res, err
	}
}

func (outcome) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (outcome) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		err := next(ctx, conn)
		record(ctx, err)
		return err
	}
}

func record(ctx context.Context, err error) {
	if info := rpcOf(ctx); info != nil {
		code := connect.Code(0)
		if err != nil {
			code = connect.CodeOf(err)
		}
		info.code.Store(int32(code))
	}
}

func codeName(c connect.Code) string {
	if c == 0 {
		return "OK"
	}
	return wire.Code(c).String()
}

// writeStatus answers a request with a gRPC status and no message ("trailers-only"): the
// status in the response headers, as gRPC allows when there is nothing else to send.
func writeStatus(w http.ResponseWriter, code connect.Code, message string) {
	hd := w.Header()
	hd.Set("Content-Type", "application/grpc")
	hd.Set("Grpc-Status", strconv.Itoa(int(code)))
	hd.Set("Grpc-Message", percentEncode(message))
	w.WriteHeader(http.StatusOK)
}

// percentEncode encodes grpc-message: every byte outside printable ASCII, and %, as %XX.
func percentEncode(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := range len(s) {
		c := s[i]
		if c >= 0x20 && c <= 0x7e && c != '%' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&15])
	}
	return b.String()
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
