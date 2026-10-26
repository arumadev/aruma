package httpx

import (
	"context"
	"log/slog"
	"net/http"
	"net/netip"
	"runtime/debug"
	"time"

	"github.com/arumadev/aruma/internal/platform/apierr"
	"github.com/arumadev/aruma/internal/platform/crypto"
)

type ctxKey int

const (
	requestIDKey ctxKey = iota
	clientIPKey
)

// RequestID returns the request's id, set by Common.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// ClientAddr returns the client address resolved by Common.
func ClientAddr(ctx context.Context) netip.Addr {
	ip, _ := ctx.Value(clientIPKey).(netip.Addr)
	return ip
}

// Common wraps every request on the public entry point: it assigns a request id, resolves the
// client address, caps the body, recovers panics as INTERNAL errors and logs one line per
// request. The query string is never logged, since tokens sometimes travel there.
func Common(next http.Handler, logger *slog.Logger, trusted []netip.Prefix) http.Handler {
	return CommonWithLimit(next, logger, trusted, MaxBodyBytes)
}

// CommonWithLimit is Common with another body cap, for the APIs whose protocol allows larger
// bodies than MaxBodyBytes (Realtime Database REST writes, ADR-019).
func CommonWithLimit(next http.Handler, logger *slog.Logger, trusted []netip.Prefix, maxBody int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		r, id, ip := Begin(w, r, trusted)
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)

		rec := &StatusRecorder{ResponseWriter: w, Status: http.StatusOK}
		defer func() {
			if p := recover(); p != nil {
				if p == http.ErrAbortHandler {
					panic(p)
				}
				logger.Error("panic", "request_id", id, "panic", p, "stack", string(debug.Stack()))
				if !rec.Wrote {
					apierr.Write(rec, apierr.New(apierr.Internal, "Internal error encountered."))
				}
			}
			logger.Info("request",
				"request_id", id,
				"method", truncate(r.Method, 16),
				"path", r.URL.Path,
				"status", rec.Status,
				"duration_ms", time.Since(start).Milliseconds(),
				"client_ip", ip.String(),
			)
		}()
		next.ServeHTTP(rec, r)
	})
}

// Begin is the start Common gives every request, for a route that cannot take the rest of it
// (the Document Database's gRPC route, whose streams need the writer's Flush and a body cap per
// message, ADR-027 §3.1): it assigns the request id, resolves the client address through trusted,
// and sets X-Request-Id. RequestID and ClientAddr read them from the returned request.
func Begin(w http.ResponseWriter, r *http.Request, trusted []netip.Prefix) (*http.Request, string, netip.Addr) {
	id := crypto.RandomHex(8)
	ip := ClientIP(r, trusted)
	ctx := context.WithValue(context.WithValue(r.Context(), requestIDKey, id), clientIPKey, ip)
	w.Header().Set("X-Request-Id", id)
	return r.WithContext(ctx), id, ip
}

// StatusRecorder captures the status code written by a handler.
type StatusRecorder struct {
	http.ResponseWriter
	Status int
	Wrote  bool
}

func (s *StatusRecorder) WriteHeader(code int) {
	if !s.Wrote {
		s.Status = code
		s.Wrote = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *StatusRecorder) Write(b []byte) (int, error) {
	s.Wrote = true
	return s.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (s *StatusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// truncate bounds client-controlled values before they reach the log.
func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
