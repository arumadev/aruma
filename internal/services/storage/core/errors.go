package core

import (
	"errors"
	"strconv"
	"strings"
)

// Error is a named Storage error. core knows no HTTP: each surface maps the errors, with
// errors.Is, to its status and body (api/errors.go, gcs/errors.go). A new error goes through the
// integrator.
type Error struct {
	code string // stable: an upload session keeps it as its final_status
	msg  string
}

func (e *Error) Error() string { return "storage: " + e.msg }

// Code returns the error's stable name.
func (e *Error) Code() string { return e.code }

var (
	// ErrBucketNotFound is a bucket that is no active project's: 404 on both surfaces, as
	// production answers /v0 (ADR-022 §2).
	ErrBucketNotFound = &Error{"bucket_not_found", "bucket not found"}
	// ErrNotFound is an object that does not exist, or a generation that is not the current one.
	ErrNotFound = &Error{"not_found", "object not found"}
	// ErrUploadNotFound is an upload id that names no session of this bucket and object.
	ErrUploadNotFound = &Error{"upload_not_found", "upload session not found"}
	// ErrPermissionDenied is a refusal by the rules, a download token that does not match, or
	// an admin-only operation by someone else.
	ErrPermissionDenied = &Error{"permission_denied", "permission denied"}
	// ErrUnauthenticated is a session's identity that no longer stands at finalize: the user was
	// deleted, disabled or revoked since the start (ADR-022 §7).
	ErrUnauthenticated = &Error{"unauthenticated", "the upload's user is no longer signed in"}
	// ErrQuotaExceeded is a write over the project's quota or the anonymous share of it: 402 on
	// /v0, 403 quotaExceeded on GCS (ADR-022 §6.5).
	ErrQuotaExceeded = &Error{"quota_exceeded", "quota exceeded"}
	// ErrTooLarge is a body or object over object_bytes, or over the declared size.
	ErrTooLarge = &Error{"too_large", "object too large"}
	// ErrPreconditionFailed is a GCS precondition that does not hold: 412 conditionNotMet.
	ErrPreconditionFailed = &Error{"precondition_failed", "precondition failed"}
	// ErrInvalidPageToken is a page token that is malformed, forged, or for another list.
	ErrInvalidPageToken = &Error{"invalid_page_token", "invalid page token"}
	// ErrSizeMismatch is a finalize whose size is not the size declared at start.
	ErrSizeMismatch = &Error{"size_mismatch", "the upload's size does not match the size declared"}
	// ErrChecksumMismatch is a client's crc32c or md5Hash that is not the object's.
	ErrChecksumMismatch = &Error{"checksum_mismatch", "the checksums do not match the data"}
	// ErrUploadFinal is a request that needs an active session on a final one, such as a cancel.
	ErrUploadFinal = &Error{"upload_final", "the upload session is already final"}
	// ErrUploadCancelled is a chunk or finalize on a cancelled session.
	ErrUploadCancelled = &Error{"upload_cancelled", "the upload session was cancelled"}
	// ErrTooManyUploads is a start over the live sessions allowed per project, user or address.
	ErrTooManyUploads = &Error{"too_many_uploads", "too many upload sessions"}
	// ErrOverloaded is a rules evaluation the process cannot afford now: 503 with a bounded
	// Retry-After, never 403 (ADR-022 §8).
	ErrOverloaded = &Error{"overloaded", "the rules engine is overloaded"}
	// ErrUnavailable is a failure of the server itself: the disk floor (min_free_bytes), the
	// database, or a commit whose outcome could not be settled (ADR-022 §5): 503.
	ErrUnavailable = &Error{"unavailable", "the service is unavailable"}
)

// namedErrors are the errors a final upload session may keep, by code.
var namedErrors = map[string]*Error{}

func init() {
	for _, e := range []*Error{ErrBucketNotFound, ErrNotFound, ErrUploadNotFound, ErrPermissionDenied,
		ErrUnauthenticated, ErrQuotaExceeded, ErrTooLarge, ErrPreconditionFailed, ErrInvalidPageToken,
		ErrSizeMismatch, ErrChecksumMismatch, ErrUploadFinal, ErrUploadCancelled, ErrTooManyUploads,
		ErrOverloaded, ErrUnavailable} {
		namedErrors[e.code] = e
	}
}

// InvalidError is a request core refuses as malformed: 400. Reason is GCS's errors[].reason
// (invalid or required); Message is shown to the client.
type InvalidError struct {
	Reason  string
	Message string
}

func (e *InvalidError) Error() string { return "storage: " + e.Message }

// invalid returns an InvalidError with reason invalid.
func invalid(msg string) error { return &InvalidError{Reason: "invalid", Message: msg} }

// OffsetError is a chunk that starts after the durable bytes: a gap. The session stays active,
// and the client learns Received and resumes with a query (ADR-022 §6.1).
type OffsetError struct {
	Received int64
}

func (e *OffsetError) Error() string {
	return "storage: the chunk starts after the " + strconv.FormatInt(e.Received, 10) + " bytes received"
}

// encodeError returns how a final upload session keeps its refusal: the error's code and its
// message. Every final refusal is a named Error or an InvalidError; anything else is kept as
// ErrUnavailable.
func encodeError(err error) (code, message string) {
	var ie *InvalidError
	if errors.As(err, &ie) {
		return "invalid:" + ie.Reason, ie.Message
	}
	var e *Error
	if errors.As(err, &e) && namedErrors[e.code] == e {
		return e.code, e.msg
	}
	return ErrUnavailable.code, ErrUnavailable.msg
}

// decodeError returns the error encodeError kept, so every repeat of a finalize answers as the
// first did.
func decodeError(code, message string) error {
	if reason, ok := strings.CutPrefix(code, "invalid:"); ok {
		return &InvalidError{Reason: reason, Message: message}
	}
	if e, ok := namedErrors[code]; ok {
		return e
	}
	return ErrUnavailable
}
