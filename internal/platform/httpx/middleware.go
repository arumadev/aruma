package httpx

import (
	"context"
	"log/slog"
	"net/http"
	"net/netip"
	"runtime/debug"
	"time"

	"github.com/osama300/aurma-project/internal/platform/apierr"
	"github.com/osama300/aurma-project/internal/platform/crypto"
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
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		id := crypto.RandomHex(8)
		ip := ClientIP(r, trusted)
		ctx := context.WithValue(context.WithValue(r.Context(), requestIDKey, id), clientIPKey, ip)
		r = r.WithContext(ctx)
		r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)
		w.Header().Set("X-Request-Id", id)

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
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.Status,
				"duration_ms", time.Since(start).Milliseconds(),
				"client_ip", ip.String(),
			)
		}()
		next.ServeHTTP(rec, r)
	})
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
