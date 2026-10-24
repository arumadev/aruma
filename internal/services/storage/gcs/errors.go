package gcs

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/arumadev/aruma/internal/services/storage/core"
	"github.com/arumadev/aruma/internal/services/storage/wire"
)

// The GCS surface's errors (B §8): {"error":{"code","message","errors":[{"domain":"global",
// "reason","message"}]}}, code a number equal to the HTTP status. Node takes err.code from the
// body, not the status, so a string or a missing code would make exists() throw on a 404
// instead of returning false (common/util.js:199-206, service-object.js:143-156).

// apiError is a refusal this surface words itself: its status, errors[].reason and message.
type apiError struct {
	status  int
	reason  string
	message string
}

func (e *apiError) Error() string { return "gcs: " + e.message }

// invalidf is a 400 with reason invalid.
func invalidf(format string, args ...any) error {
	return &apiError{status: http.StatusBadRequest, reason: wire.ReasonInvalid, message: fmt.Sprintf(format, args...)}
}

// requiredf is a 400 with reason required: a parameter or field that must be there.
func requiredf(format string, args ...any) error {
	return &apiError{status: http.StatusBadRequest, reason: wire.ReasonRequired, message: fmt.Sprintf(format, args...)}
}

var (
	// errDeferred is what v0.3 does not serve: ACLs, IAM, compose, bucket creation and the rest
	// of ADR-023 §2.
	errDeferred = &apiError{status: http.StatusNotImplemented, reason: wire.ReasonNotImplemented,
		message: "This operation is not implemented by Aruma."}
	// errNotFoundPath is a path or a method the JSON API does not have.
	errNotFoundPath = &apiError{status: http.StatusNotFound, reason: wire.ReasonNotFound, message: "Not Found"}
)

// errTooManyWrites is a client over upload_requests_per_ip (ADR-022 §17): passing pressure,
// which the SDKs retry on 429 (B §8).
var errTooManyWrites = &apiError{status: http.StatusTooManyRequests, reason: "rateLimitExceeded",
	message: "Too many requests in flight from this address."}

// retryAfter is the Retry-After of passing pressure alone, in seconds, and bounded: too many
// writes in flight from one address (errTooManyWrites), a rules engine over its budget
// (core.ErrOverloaded), and an upload start over its rate, whose handler sets the wait
// AllowUploadStart gives before it calls writeError. A client that honours
// Retry-After may drop its own caps on retries (gtm-session-fetcher skips its maximum interval
// and total time, GTMSessionFetcher.m:3682, :3854), so a 503 of the server itself
// (core.ErrUnavailable: the database, the disk floor) gets none, and the client's usual backoff
// ends.
const retryAfter = "1"

// pressure reports whether err is passing pressure, whose reply carries a Retry-After.
func pressure(err error) bool {
	return errors.Is(err, errTooManyWrites) || errors.Is(err, core.ErrOverloaded)
}

// writeError answers err in GCS's shape. bucket and name word a not-found: "No such object:
// <bucket>/<name>", or the bucket's message when name is "". An error core does not name is the
// server's, logged and answered 500.
func (a *API) writeError(w http.ResponseWriter, err error, bucket, name string) {
	e := a.gcsError(err, bucket, name)
	if e.status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer realm="aruma"`)
	}
	if pressure(err) && w.Header().Get("Retry-After") == "" {
		w.Header().Set("Retry-After", retryAfter)
	}
	a.writeJSON(w, e.status, wire.NewGCSError(e.status, e.reason, e.message))
}

// gcsError maps an error to its status, reason and message.
func (a *API) gcsError(err error, bucket, name string) *apiError {
	if e, ok := errors.AsType[*apiError](err); ok {
		return e
	}
	if e, ok := errors.AsType[*core.InvalidError](err); ok {
		reason := e.Reason
		if reason == "" {
			reason = wire.ReasonInvalid
		}
		return &apiError{http.StatusBadRequest, reason, e.Message}
	}
	if e, ok := errors.AsType[*core.OffsetError](err); ok {
		return &apiError{http.StatusBadRequest, wire.ReasonInvalid,
			fmt.Sprintf("The request starts after the %d bytes received.", e.Received)}
	}
	switch {
	case errors.Is(err, core.ErrBucketNotFound):
		return &apiError{http.StatusNotFound, wire.ReasonNotFound, wire.MessageNoSuchBucket}
	case errors.Is(err, core.ErrNotFound):
		if name == "" {
			return &apiError{http.StatusNotFound, wire.ReasonNotFound, wire.MessageNoSuchBucket}
		}
		return &apiError{http.StatusNotFound, wire.ReasonNotFound, wire.MessageNoSuchObject(bucket, name)}
	case errors.Is(err, core.ErrUploadNotFound):
		return &apiError{http.StatusNotFound, wire.ReasonNotFound, "No such upload session."}
	case errors.Is(err, core.ErrPreconditionFailed):
		return &apiError{http.StatusPreconditionFailed, wire.ReasonConditionNotMet,
			"At least one of the pre-conditions you specified did not hold."}
	case errors.Is(err, core.ErrPermissionDenied):
		return &apiError{http.StatusForbidden, wire.ReasonForbidden, "Permission denied."}
	case errors.Is(err, core.ErrUnauthenticated):
		return &apiError{http.StatusUnauthorized, wire.ReasonRequired, "The upload's credential no longer stands."}
	case errors.Is(err, core.ErrQuotaExceeded):
		// 403 quotaExceeded, not /v0's 402 (ADR-023 §3-25).
		return &apiError{http.StatusForbidden, wire.ReasonQuotaExceeded, "The project's storage quota is exceeded."}
	case errors.Is(err, core.ErrTooLarge):
		return &apiError{http.StatusRequestEntityTooLarge, "uploadTooLarge", "The object is larger than this server allows."}
	case errors.Is(err, core.ErrInvalidPageToken):
		return &apiError{http.StatusBadRequest, wire.ReasonInvalid, "Invalid page token."}
	case errors.Is(err, core.ErrSizeMismatch):
		return &apiError{http.StatusBadRequest, wire.ReasonInvalid, "The object's size is not the size declared."}
	case errors.Is(err, core.ErrChecksumMismatch):
		return &apiError{http.StatusBadRequest, wire.ReasonInvalid, "The checksums provided do not match the data."}
	case errors.Is(err, core.ErrUploadFinal):
		return &apiError{http.StatusBadRequest, wire.ReasonInvalid, "The upload session is already final."}
	case errors.Is(err, core.ErrUploadCancelled):
		return &apiError{http.StatusGone, wire.ReasonNotFound, "The upload session was cancelled."}
	case errors.Is(err, core.ErrInvalidArgument):
		return &apiError{http.StatusBadRequest, wire.ReasonInvalid, "Invalid argument."}
	case errors.Is(err, core.ErrTooManyUploads):
		return &apiError{http.StatusTooManyRequests, "rateLimitExceeded", "Too many upload sessions."}
	case errors.Is(err, core.ErrOverloaded), errors.Is(err, core.ErrUnavailable):
		if errors.Is(err, core.ErrUnavailable) {
			a.log.Warn("gcs: unavailable", "error", err)
		}
		return &apiError{http.StatusServiceUnavailable, wire.ReasonBackendError, "The service is currently unavailable."}
	}
	a.log.Error("gcs: internal error", "error", err)
	return &apiError{http.StatusInternalServerError, wire.ReasonBackendError, "Internal error."}
}
