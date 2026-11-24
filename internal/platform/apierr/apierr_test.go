package apierr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteRendersGoogleRPCStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	Write(rec, Invalid("projectId", "projectId must be 6 to 30 characters."))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	var body struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Status  string `json:"status"`
			Details []struct {
				Type            string `json:"@type"`
				FieldViolations []struct {
					Field string `json:"field"`
				} `json:"fieldViolations"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	e := body.Error
	if e.Code != 400 || e.Status != "INVALID_ARGUMENT" || e.Message == "" {
		t.Fatalf("unexpected error body: %+v", e)
	}
	if len(e.Details) != 1 || e.Details[0].Type != "type.googleapis.com/google.rpc.BadRequest" ||
		e.Details[0].FieldViolations[0].Field != "projectId" {
		t.Fatalf("unexpected details: %+v", e.Details)
	}
}

func TestFromHidesInternalErrors(t *testing.T) {
	err := From(errors.New("pq: password authentication failed for user secret"))
	if err.Code != Internal {
		t.Fatalf("code = %v, want INTERNAL", err.Code)
	}
	body, _ := json.Marshal(err.Body())
	if string(body) != `{"error":{"code":500,"message":"Internal error encountered.","status":"INTERNAL"}}` {
		t.Fatalf("leaked internals: %s", body)
	}
}

func TestCodeMapping(t *testing.T) {
	cases := map[Code]int{
		NotFound: 404, AlreadyExists: 409, Unauthenticated: 401, PermissionDenied: 403,
		FailedPrecondition: 400, ResourceExhausted: 429, Unimplemented: 501, Aborted: 409,
	}
	for code, want := range cases {
		if got := code.HTTPStatus(); got != want {
			t.Errorf("%v.HTTPStatus() = %d, want %d", code, got, want)
		}
	}
}

func TestWrapKeepsCauseForLogs(t *testing.T) {
	cause := errors.New("boom")
	err := Wrap(cause, Unavailable, "Try again later.")
	if !errors.Is(err, cause) {
		t.Fatal("cause lost")
	}
	if CodeOf(err) != Unavailable {
		t.Fatal("code lost")
	}
}

type sqlStateErr string

func (e sqlStateErr) Error() string    { return "pg: " + string(e) }
func (e sqlStateErr) SQLState() string { return string(e) }

type connectErr struct{}

func (connectErr) Error() string     { return "failed to connect to `host=127.0.0.1`: connection refused" }
func (connectErr) SafeToRetry() bool { return true }

// doneErr is pgconn's error for a query whose context had ended before it was sent.
type doneErr struct{}

func (doneErr) Error() string     { return "context already done" }
func (doneErr) SafeToRetry() bool { return true }

// A database that is down is UNAVAILABLE (503, which SDKs retry), not INTERNAL (500).
// dbTimeoutErr is store.ErrTimeout as apierr sees it, through its interface.
type dbTimeoutErr struct{}

func (dbTimeoutErr) Error() string         { return "the database did not answer in time" }
func (dbTimeoutErr) DatabaseTimeout() bool { return true }

func TestDatabaseOutageIsUnavailable(t *testing.T) {
	for name, cause := range map[string]error{
		"connect failed":    connectErr{},
		"wrapped":           fmt.Errorf("query: %w", connectErr{}),
		"admin shutdown":    sqlStateErr("57P01"),
		"cannot connect":    sqlStateErr("57P03"),
		"too many conns":    sqlStateErr("53300"),
		"connection failed": sqlStateErr("08006"),
		"net error":         &net.OpError{Op: "read", Err: errors.New("connection reset by peer")},
		// A pooled connection Postgres (or the network) closed without a word: a killed
		// server, a restarted proxy.
		"connection closed": fmt.Errorf("query: %w", io.ErrUnexpectedEOF),
		// The pool's connect_timeout, on the pool's own context.
		"connect timed out":  fmt.Errorf("failed to connect to `user=aruma`: %w", errors.Join(errors.New("timeout"), context.DeadlineExceeded)),
		"database timeout":   dbTimeoutErr{},
		"wrapped db timeout": fmt.Errorf("begin: %w", dbTimeoutErr{}),
	} {
		if got := CodeOf(Wrap(cause, Internal, "Internal error encountered.")); got != Unavailable {
			t.Errorf("%s: Wrap code = %v, want UNAVAILABLE", name, got)
		}
		if got := CodeOf(cause); got != Unavailable {
			t.Errorf("%s: From code = %v, want UNAVAILABLE", name, got)
		}
		if st := From(cause).HTTPStatus(); st != http.StatusServiceUnavailable {
			t.Errorf("%s: status = %d, want 503", name, st)
		}
	}
	for name, cause := range map[string]error{
		"unique violation": sqlStateErr("23505"),
		"plain":            errors.New("boom"),
		// The driver marks a query whose context had already ended as safe to retry.
		"caller gave up":  fmt.Errorf("query: %w", errors.Join(doneErr{}, context.Canceled)),
		"caller deadline": fmt.Errorf("query: %w", errors.Join(doneErr{}, context.DeadlineExceeded)),
		// A connect the caller gave up on (the pool's own connects are not the caller's).
		"caller gave up connecting": fmt.Errorf("query: %w", errors.Join(connectErr{}, context.Canceled)),
	} {
		if got := CodeOf(Wrap(cause, Internal, "x")); got != Internal {
			t.Errorf("%s: code = %v, want INTERNAL", name, got)
		}
	}
}

// bodyErr is a failure to read a request's body, as httpx marks them.
type bodyErr struct{ error }

func (bodyErr) RequestBody() bool { return true }
func (e bodyErr) Unwrap() error   { return e.error }

// TestRequestBodyFailureIsNotAnOutage: a body the client cut ends with the errors a lost
// database connection ends with, and is the client's failure, not UNAVAILABLE.
func TestRequestBodyFailureIsNotAnOutage(t *testing.T) {
	for name, cause := range map[string]error{
		"body cut short":   fmt.Errorf("read: %w", bodyErr{io.ErrUnexpectedEOF}),
		"connection reset": bodyErr{&net.OpError{Op: "read", Err: errors.New("connection reset by peer")}},
	} {
		if IsUnavailable(cause) {
			t.Errorf("%s: taken for a database outage", name)
		}
		if got := CodeOf(cause); got != InvalidArgument {
			t.Errorf("%s: From code = %v, want INVALID_ARGUMENT", name, got)
		}
	}
}
