package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/arumadev/aruma/internal/platform/idtoken"
	"github.com/arumadev/aruma/internal/services/storage/core"
	"github.com/arumadev/aruma/internal/services/storage/wire"
)

// /v0's errors (ADR-022 §12): {"error":{"code":<status>,"message":"..."}}, compact, as
// application/json; charset=UTF-8. No client reads the body (A §5.1): the status alone becomes
// the SDK's error code (A §5.2), so the statuses are the contract and the messages are for
// people, production's where it is known (wire.Message*). Never 409, which Android shows as
// invalid-checksum, and never 308.

// httpError is a refusal of this surface that core does not name: a status and its message.
type httpError struct {
	status  int
	message string
}

func (e *httpError) Error() string { return "storage api: " + e.message }

// statusError returns an error writeError answers with status and message, for the handlers'
// own refusals (a malformed body or header).
func statusError(status int, message string) error { return &httpError{status, message} }

var (
	// errBadRoute is a path or method that names no /v0 operation, as production words it
	// (bucket_probe.json: /v0/b/ and /v0/nothing-here).
	errBadRoute = statusError(http.StatusBadRequest, wire.MessageBadMethodOrURL)
	// errUnauthenticated is a credential that does not verify (A §7): 401, which every SDK reads
	// as unauthenticated.
	errUnauthenticated = statusError(http.StatusUnauthorized, "Invalid authentication credentials.")
	// errBearerRefused is a Bearer that is no admin of the bucket's project: 403, as the
	// conformance tests expect of production (ADR-023 §3-67). A Firebase token that does not
	// verify stays errUnauthenticated, which the SDKs answer by refreshing it.
	errBearerRefused = statusError(http.StatusForbidden, wire.MessagePermission)
	// errTooManyWrites is a client over upload_requests_per_ip: pressure that passes, so 503 with
	// a bounded Retry-After, which iOS retries where it does not retry 429 (A §5.3).
	errTooManyWrites = statusError(http.StatusServiceUnavailable, "Too many requests in flight from this address.")
)

// Retry-After goes on the 503s of passing pressure alone (A §5.3, ADR-022 §12): too many writes
// in flight from one address (errTooManyWrites), a rules engine over its budget
// (core.ErrOverloaded), and an upload start over its rate (writeRetry). iOS's fetcher skips its
// maximum interval and its total time when a reply carries one (GTMSessionFetcher.m:3682, :3854),
// so on any other 503 (core.ErrUnavailable: the database, the disk floor) it would retry forever;
// there it gets none and its usual backoff ends. The value is bounded besides.
const (
	retryAfter    = 5 * time.Second
	maxRetryAfter = 60 * time.Second
)

// writeError answers a request with err in /v0's shape. The errors of core map by errors.Is, in
// the order below: a quota refusal that also names a session limit is the 402 it was made
// (core's errTooManyLive). Headers set before the call stay, so an upload handler sets
// X-Goog-Upload-Status first. An error nobody named is logged and answered 500.
func (a *API) writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, message := a.errorStatus(r, err)
	if pressure(err) && w.Header().Get("Retry-After") == "" {
		setRetryAfter(w.Header(), retryAfter)
	}
	writeJSON(w, status, wire.NewFirebaseError(status, message))
}

// writeRetry answers with status (503, or 429 for a rate the caller knows iOS need not retry)
// and a Retry-After of wait, bounded to [1s, 60s]: for core.Service.AllowUploadStart's refusal.
func (a *API) writeRetry(w http.ResponseWriter, r *http.Request, status int, wait time.Duration) {
	setRetryAfter(w.Header(), wait)
	a.writeError(w, r, statusError(status, http.StatusText(status)+"."))
}

// pressure reports whether err is passing pressure, whose 503 carries a Retry-After.
func pressure(err error) bool {
	return errors.Is(err, errTooManyWrites) || errors.Is(err, core.ErrOverloaded)
}

func setRetryAfter(h http.Header, wait time.Duration) {
	secs := int64(min(max(wait, time.Second), maxRetryAfter) / time.Second)
	h.Set("Retry-After", strconv.FormatInt(secs, 10))
}

// errorStatus maps err to its status and message.
func (a *API) errorStatus(r *http.Request, err error) (int, string) {
	if e, ok := errors.AsType[*httpError](err); ok {
		return e.status, e.message
	}
	if e, ok := errors.AsType[*core.InvalidError](err); ok {
		return http.StatusBadRequest, e.Message
	}
	if _, ok := errors.AsType[*core.OffsetError](err); ok {
		// A gap: the upload handler adds X-Goog-Upload-Status: active and the size received.
		return http.StatusBadRequest, "The upload offset is not the size received."
	}
	if idtoken.Status(err) != "" {
		return http.StatusUnauthorized, errUnauthenticated.(*httpError).message
	}
	for _, m := range errorStatuses {
		if errors.Is(err, m.err) {
			if m.status >= http.StatusInternalServerError {
				a.log.Warn("storage: request failed", "method", r.Method, "path", r.URL.Path, "error", err)
			}
			return m.status, m.message
		}
	}
	a.log.Error("storage: request failed", "method", r.Method, "path", r.URL.Path, "error", err)
	return http.StatusInternalServerError, "Internal error."
}

// errorStatuses are core's named errors and their /v0 replies, in the order they are tried.
var errorStatuses = []struct {
	err     error
	status  int
	message string
}{
	{core.ErrBucketNotFound, http.StatusNotFound, wire.MessageNotFound},
	{core.ErrNotFound, http.StatusNotFound, wire.MessageNotFound},
	{core.ErrUploadNotFound, http.StatusNotFound, wire.MessageNotFound},
	{core.ErrPermissionDenied, http.StatusForbidden, wire.MessagePermission},
	{core.ErrUnauthenticated, http.StatusUnauthorized, "The upload's user is no longer signed in."},
	// 402 is quota-exceeded in JS and iOS (A §5.2), and before ErrTooManyUploads: a session's
	// first byte over the live sessions is both, and terminal as 402 (core's errTooManyLive).
	{core.ErrQuotaExceeded, http.StatusPaymentRequired, "Quota exceeded."},
	{core.ErrTooLarge, http.StatusRequestEntityTooLarge, "The object is too large."},
	{core.ErrSizeMismatch, http.StatusBadRequest, "The upload's size is not the size declared."},
	{core.ErrChecksumMismatch, http.StatusBadRequest, "The checksums do not match the data."},
	{core.ErrInvalidPageToken, http.StatusBadRequest, "Invalid page token."},
	{core.ErrInvalidArgument, http.StatusBadRequest, "Invalid argument."},
	{core.ErrUploadFinal, http.StatusBadRequest, "The upload is already final."},
	{core.ErrUploadCancelled, http.StatusBadRequest, "The upload was cancelled."},
	// A /v0 request carries no preconditions (core refuses them, ErrInvalidArgument), so this
	// only answers one that reached core some other way.
	{core.ErrPreconditionFailed, http.StatusPreconditionFailed, "Precondition failed."},
	{core.ErrTooManyUploads, http.StatusTooManyRequests, "Too many upload sessions."},
	{core.ErrOverloaded, http.StatusServiceUnavailable, "The service is overloaded; try again."},
	{core.ErrUnavailable, http.StatusServiceUnavailable, "The service is unavailable; try again."},
}

// writeJSON writes v as the reply, compact and without HTML escaping (wire.Marshal), with the
// headers production sends on every /v0 JSON reply (E §1): nosniff, and private, max-age=0.
func writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := wire.Marshal(v)
	if err != nil {
		// Every value written here is a wire type of strings and maps of strings.
		panic("storage api: encode a reply: " + err.Error())
	}
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=UTF-8")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "private, max-age=0")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
