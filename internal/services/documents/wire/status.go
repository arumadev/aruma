package wire

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/arumadev/aruma/internal/services/documents/index"
)

// Code is a google.rpc.Code: the gRPC status code, which REST names in "status" and maps to an
// HTTP status.
type Code uint32

// The codes, numbered as google.rpc.Code and gRPC number them.
const (
	CodeOK Code = iota
	CodeCanceled
	CodeUnknown
	CodeInvalidArgument
	CodeDeadlineExceeded
	CodeNotFound
	CodeAlreadyExists
	CodePermissionDenied
	CodeResourceExhausted
	CodeFailedPrecondition
	CodeAborted
	CodeOutOfRange
	CodeUnimplemented
	CodeInternal
	CodeUnavailable
	CodeDataLoss
	CodeUnauthenticated
)

var codeNames = [...]string{"OK", "CANCELLED", "UNKNOWN", "INVALID_ARGUMENT", "DEADLINE_EXCEEDED", "NOT_FOUND",
	"ALREADY_EXISTS", "PERMISSION_DENIED", "RESOURCE_EXHAUSTED", "FAILED_PRECONDITION", "ABORTED", "OUT_OF_RANGE",
	"UNIMPLEMENTED", "INTERNAL", "UNAVAILABLE", "DATA_LOSS", "UNAUTHENTICATED"}

// String is the code's name as REST's "status" carries it: INVALID_ARGUMENT, CANCELLED.
func (c Code) String() string {
	if int(c) < len(codeNames) {
		return codeNames[c]
	}
	return "Code(" + strconv.FormatUint(uint64(c), 10) + ")"
}

// codeHTTP is Google's mapping of a code to an HTTP status (google/rpc/code.proto), which
// production's REST answers follow: 400 INVALID_ARGUMENT and FAILED_PRECONDITION, 404
// NOT_FOUND, 409 ABORTED (f0-errors.json, f0-missing-database.json, indexes-105.json).
var codeHTTP = [...]int{
	CodeOK:                 http.StatusOK,
	CodeCanceled:           499,
	CodeUnknown:            http.StatusInternalServerError,
	CodeInvalidArgument:    http.StatusBadRequest,
	CodeDeadlineExceeded:   http.StatusGatewayTimeout,
	CodeNotFound:           http.StatusNotFound,
	CodeAlreadyExists:      http.StatusConflict,
	CodePermissionDenied:   http.StatusForbidden,
	CodeResourceExhausted:  http.StatusTooManyRequests,
	CodeFailedPrecondition: http.StatusBadRequest,
	CodeAborted:            http.StatusConflict,
	CodeOutOfRange:         http.StatusBadRequest,
	CodeUnimplemented:      http.StatusNotImplemented,
	CodeInternal:           http.StatusInternalServerError,
	CodeUnavailable:        http.StatusServiceUnavailable,
	CodeDataLoss:           http.StatusInternalServerError,
	CodeUnauthenticated:    http.StatusUnauthorized,
}

// HTTPStatus is the HTTP status REST answers the code with; 500 for a code out of range.
func (c Code) HTTPStatus() int {
	if int(c) < len(codeHTTP) {
		return codeHTTP[c]
	}
	return http.StatusInternalServerError
}

// Error is the Document Database's one error form: a status code, production's message byte for
// byte, and optional google.rpc.Status details. One Error renders on both surfaces:
//
//   - gRPC (Connect): the code, the message as grpc-message, and the details in
//     grpc-status-details-bin, which is absent when there are none;
//   - REST: Code.HTTPStatus and Google's body {"error":{"code","message","status"[,"details"]}},
//     wrapped in a one-element array on the streaming methods (batchGet, runQuery,
//     runAggregationQuery).
//
// Where production's two surfaces differ, WithGRPC sets gRPC's own message and details: a
// database that does not exist is NOT_FOUND with a text on REST and an empty message on gRPC
// (f0-missing-database.json), and the needs-index error carries its link as a google.rpc.Help
// on gRPC alone (ADR-027 §1.1, §5.1).
type Error struct {
	Code    Code
	Message string
	// Details are google.rpc.Status details (errdetails messages), sent on both surfaces.
	Details []proto.Message

	grpc *grpcForm
}

// grpcForm is the message and details gRPC carries in place of REST's.
type grpcForm struct {
	message string
	details []proto.Message
}

// NewError returns an error of code with message and details.
func NewError(code Code, message string, details ...proto.Message) *Error {
	return &Error{Code: code, Message: message, Details: details}
}

func (e *Error) Error() string {
	if e.Message == "" {
		return e.Code.String()
	}
	return e.Code.String() + ": " + e.Message
}

// WithGRPC returns a copy of e that carries message and details on gRPC instead of e's.
func (e *Error) WithGRPC(message string, details ...proto.Message) *Error {
	c := *e
	c.grpc = &grpcForm{message: message, details: details}
	return &c
}

// GRPCMessage is the message gRPC carries.
func (e *Error) GRPCMessage() string {
	if e.grpc != nil {
		return e.grpc.message
	}
	return e.Message
}

// GRPCDetails are the details gRPC carries.
func (e *Error) GRPCDetails() []proto.Message {
	if e.grpc != nil {
		return e.grpc.details
	}
	return e.Details
}

// Connect is e as a Connect error, which connect-go writes as grpc-status, grpc-message and,
// when there are details, grpc-status-details-bin.
func (e *Error) Connect() *connect.Error {
	var underlying error
	if msg := e.GRPCMessage(); msg != "" {
		underlying = errors.New(msg)
	}
	ce := connect.NewError(connect.Code(e.Code), underlying)
	for _, d := range e.GRPCDetails() {
		if ed, err := connect.NewErrorDetail(d); err == nil {
			ce.AddDetail(ed)
		}
	}
	return ce
}

// HTTPStatus is the HTTP status of e's REST answer.
func (e *Error) HTTPStatus() int { return e.Code.HTTPStatus() }

// restError is the object under "error" in a REST error body, in Google's field order.
type restError struct {
	Code    int               `json:"code"`
	Message string            `json:"message"`
	Status  string            `json:"status"`
	Details []json.RawMessage `json:"details,omitempty"`
}

// RESTBody is e's REST error body: {"error":{"code":400,"message":…,"status":…}}, with
// "details" when e has them, each a google.protobuf.Any in proto3 JSON ("@type" first).
func (e *Error) RESTBody() []byte {
	body := struct {
		Error restError `json:"error"`
	}{restError{Code: e.HTTPStatus(), Message: e.Message, Status: e.Code.String()}}
	for _, d := range e.Details {
		a, err := anypb.New(d)
		if err != nil {
			continue
		}
		b, err := protojson.Marshal(a)
		if err != nil {
			continue
		}
		body.Error.Details = append(body.Error.Details, compactJSON(b))
	}
	return marshalJSON(body)
}

// RESTStreamBody is e's body on a streaming REST method: RESTBody in a one-element array, as
// production answers batchGet, runQuery and runAggregationQuery (f0-missing-database.json,
// f0-errors.json, indexes-105.json).
func (e *Error) RESTStreamBody() []byte {
	b := e.RESTBody()
	out := make([]byte, 0, len(b)+2)
	out = append(out, '[')
	out = append(out, b...)
	return append(out, ']')
}

// RESTContentType is the Content-Type of every REST answer, errors included.
const RESTContentType = "application/json; charset=UTF-8"

// WriteREST answers a REST request with err (From maps any error): its HTTP status and body,
// the body array-wrapped when stream is set.
func WriteREST(w http.ResponseWriter, err error, stream bool) {
	e := From(err)
	body := e.RESTBody()
	if stream {
		body = e.RESTStreamBody()
	}
	w.Header().Set("Content-Type", RESTContentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(e.HTTPStatus())
	_, _ = w.Write(body)
}

// From returns err as an *Error:
//
//   - an *Error (or one wrapped) as it is, and a *TargetError's;
//   - the needs-index error of package index as IndexError makes it;
//   - a Connect error by its code and message;
//   - context.Canceled as CANCELLED and context.DeadlineExceeded as DEADLINE_EXCEEDED;
//   - anything else as INTERNAL with Google's generic text, so no internal detail leaks.
//
// From(nil) is nil.
func From(err error) *Error {
	if err == nil {
		return nil
	}
	if e, ok := errors.AsType[*Error](err); ok {
		return e
	}
	if te, ok := errors.AsType[*TargetError](err); ok {
		return te.Err
	}
	if ie, ok := errors.AsType[*index.Error](err); ok {
		return IndexError(ie)
	}
	if ce, ok := errors.AsType[*connect.Error](err); ok {
		return NewError(Code(ce.Code()), ce.Message())
	}
	switch {
	case errors.Is(err, context.Canceled):
		return NewError(CodeCanceled, msgCanceled)
	case errors.Is(err, context.DeadlineExceeded):
		return NewError(CodeDeadlineExceeded, msgDeadlineExceeded)
	}
	return Internal()
}

// marshalJSON encodes v without escaping <, > and &, which the console links hold.
func marshalJSON(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return []byte(`{"error":{"code":500,"message":"Internal error encountered.","status":"INTERNAL"}}`)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

// compactJSON removes the insignificant whitespace protojson adds at random.
func compactJSON(b []byte) []byte {
	var buf bytes.Buffer
	if err := json.Compact(&buf, b); err != nil {
		return b
	}
	return buf.Bytes()
}
