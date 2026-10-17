// Package apierr is the error model shared by every Aruma API: canonical codes rendered as
// google.rpc.Status JSON, the shape Google client libraries already parse.
package apierr

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// Code is a canonical status code (google.rpc.Code).
type Code int

const (
	OK                 Code = 0
	Canceled           Code = 1
	Unknown            Code = 2
	InvalidArgument    Code = 3
	DeadlineExceeded   Code = 4
	NotFound           Code = 5
	AlreadyExists      Code = 6
	PermissionDenied   Code = 7
	ResourceExhausted  Code = 8
	FailedPrecondition Code = 9
	Aborted            Code = 10
	OutOfRange         Code = 11
	Unimplemented      Code = 12
	Internal           Code = 13
	Unavailable        Code = 14
	DataLoss           Code = 15
	Unauthenticated    Code = 16
)

var codeNames = [...]string{
	"OK", "CANCELLED", "UNKNOWN", "INVALID_ARGUMENT", "DEADLINE_EXCEEDED", "NOT_FOUND",
	"ALREADY_EXISTS", "PERMISSION_DENIED", "RESOURCE_EXHAUSTED", "FAILED_PRECONDITION",
	"ABORTED", "OUT_OF_RANGE", "UNIMPLEMENTED", "INTERNAL", "UNAVAILABLE", "DATA_LOSS",
	"UNAUTHENTICATED",
}

var httpStatus = [...]int{
	http.StatusOK, 499, http.StatusInternalServerError, http.StatusBadRequest,
	http.StatusGatewayTimeout, http.StatusNotFound, http.StatusConflict, http.StatusForbidden,
	http.StatusTooManyRequests, http.StatusBadRequest, http.StatusConflict, http.StatusBadRequest,
	http.StatusNotImplemented, http.StatusInternalServerError, http.StatusServiceUnavailable,
	http.StatusInternalServerError, http.StatusUnauthorized,
}

// String returns the status name, e.g. "NOT_FOUND".
func (c Code) String() string {
	if c < 0 || int(c) >= len(codeNames) {
		return "UNKNOWN"
	}
	return codeNames[c]
}

// HTTPStatus maps the code to its HTTP status per the Google API design guide.
func (c Code) HTTPStatus() int {
	if c < 0 || int(c) >= len(httpStatus) {
		return http.StatusInternalServerError
	}
	return httpStatus[c]
}

// Error is an API error. Message is shown to the caller, so it never carries secrets or internals.
type Error struct {
	Code    Code
	Message string
	Details []any
	cause   error
	// status overrides the HTTP status derived from Code, for transport-level errors.
	status int
}

func (e *Error) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.cause }

// New returns an error with a caller-facing message.
func New(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Wrap attaches an internal cause that is logged but never sent to the caller.
func Wrap(cause error, code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...), cause: cause}
}

// WithDetails returns a copy of e carrying extra google.rpc detail messages.
func (e *Error) WithDetails(details ...any) *Error {
	c := *e
	c.Details = append(append([]any(nil), e.Details...), details...)
	return &c
}

// From converts any error into an *Error. Errors that are not already API errors become
// INTERNAL with a generic message, so internals never leak.
func From(err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return &Error{Code: Internal, Message: "Internal error encountered.", cause: err}
}

// CodeOf returns the code of err, OK for nil.
func CodeOf(err error) Code {
	if err == nil {
		return OK
	}
	return From(err).Code
}

// FieldViolation is one entry of google.rpc.BadRequest.
type FieldViolation struct {
	Field       string `json:"field"`
	Description string `json:"description"`
}

// BadRequest builds a google.rpc.BadRequest detail.
func BadRequest(violations ...FieldViolation) any {
	return struct {
		Type            string           `json:"@type"`
		FieldViolations []FieldViolation `json:"fieldViolations"`
	}{"type.googleapis.com/google.rpc.BadRequest", violations}
}

// PayloadTooLarge is returned when a request body exceeds the limit (HTTP 413).
func PayloadTooLarge() *Error {
	return &Error{Code: InvalidArgument, Message: "Request payload is too large.", status: http.StatusRequestEntityTooLarge}
}

// HTTPStatus returns the HTTP status the error is sent with.
func (e *Error) HTTPStatus() int {
	if e.status != 0 {
		return e.status
	}
	return e.Code.HTTPStatus()
}

// Invalid is shorthand for an INVALID_ARGUMENT error about one field.
func Invalid(field, description string) *Error {
	return New(InvalidArgument, "%s", description).WithDetails(BadRequest(FieldViolation{field, description}))
}

type statusBody struct {
	Error status `json:"error"`
}

type status struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  string `json:"status"`
	Details []any  `json:"details,omitempty"`
}

// Body returns the google.rpc.Status envelope for e.
func (e *Error) Body() any {
	return statusBody{status{Code: e.HTTPStatus(), Message: e.Message, Status: e.Code.String(), Details: e.Details}}
}

// Write renders err as a google.rpc.Status JSON response.
func Write(w http.ResponseWriter, err error) {
	e := From(err)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(e.HTTPStatus())
	_ = json.NewEncoder(w).Encode(e.Body())
}
