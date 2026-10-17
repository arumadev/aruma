package apierr

import (
	"encoding/json"
	"errors"
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
