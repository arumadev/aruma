package wire

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/genproto/googleapis/rpc/errdetails"

	"github.com/arumadev/aruma/internal/services/documents/index"
)

var statusCodes = map[string]Code{}

func init() {
	for c := CodeOK; c <= CodeUnauthenticated; c++ {
		statusCodes[c.String()] = c
	}
}

// recordedError is a REST error production answered, with its whole body.
type recordedError struct {
	id          string
	http        int
	body        any
	bodyIsArray bool
}

func restErrors(t *testing.T) []recordedError {
	t.Helper()
	var out []recordedError
	for _, file := range []string{"f0-errors.json", "f0-missing-database.json", "f0-explain.json", "f0-find-nearest.json",
		"f0-descendants.json", "f0-list-show-missing.json", "f0-contention.json"} {
		var corpus struct{ Cases []f0Case }
		readCorpus(t, file, &corpus)
		for _, c := range corpus.Cases {
			if c.Transport == "rest" || strings.HasPrefix(c.ID, "rest.") || strings.HasPrefix(c.ID, "admin.") {
				if c.Response.HTTP >= 400 {
					out = append(out, recordedError{file + "/" + c.ID, c.Response.HTTP, c.Response.Body, c.Response.BodyIsArray})
				}
			}
		}
	}
	return out
}

// TestRESTErrorBodiesCorpus renders every REST error production answered in the f0 recordings
// from its code and message, and compares status and body: Google's JSON form, array-wrapped
// exactly on the streaming methods. "Index entry is too large." carries its ErrorInfo.
func TestRESTErrorBodiesCorpus(t *testing.T) {
	cases := restErrors(t)
	if len(cases) < 30 {
		t.Fatalf("only %d REST errors recorded", len(cases))
	}
	for _, c := range cases {
		body := c.body
		if c.bodyIsArray {
			arr, ok := body.([]any)
			if !ok || len(arr) != 1 {
				t.Errorf("%s: array body %v", c.id, body)
				continue
			}
			body = arr[0]
		}
		obj, _ := body.(map[string]any)["error"].(map[string]any)
		status, _ := obj["status"].(string)
		message, _ := obj["message"].(string)
		code, ok := statusCodes[status]
		if !ok {
			t.Errorf("%s: status %q", c.id, status)
			continue
		}
		e := NewError(code, message)
		if message == "Index entry is too large." {
			e = IndexEntryTooLarge()
		}
		if e.HTTPStatus() != c.http {
			t.Errorf("%s: HTTP %d, production %d", c.id, e.HTTPStatus(), c.http)
		}
		got := e.RESTBody()
		if c.bodyIsArray {
			got = e.RESTStreamBody()
		}
		if want := toJSON(t, c.body); !sameJSON(t, got, want) {
			t.Errorf("%s:\n got  %s\n want %s", c.id, got, want)
		}
	}
}

// TestMissingDatabaseCorpus checks the three recorded answers for a database that does not
// exist (f0-missing-database.json), with the constructors core uses.
func TestMissingDatabaseCorpus(t *testing.T) {
	var corpus struct{ Cases []f0Case }
	readCorpus(t, "f0-missing-database.json", &corpus)
	db := Database{corpusProject, "aruma-ref-nonexistent"}
	for _, c := range corpus.Cases {
		switch {
		case strings.HasPrefix(c.ID, "admin."):
			e := MissingDatabaseAdmin(db)
			if e.HTTPStatus() != c.Response.HTTP || !sameJSON(t, e.RESTBody(), toJSON(t, c.Response.Body)) {
				t.Errorf("%s: %d %s", c.ID, e.HTTPStatus(), e.RESTBody())
			}
		case c.Transport == "rest":
			e := MissingDatabase(db)
			got := e.RESTBody()
			if c.Response.BodyIsArray {
				got = e.RESTStreamBody()
			}
			if e.HTTPStatus() != c.Response.HTTP || !sameJSON(t, got, toJSON(t, c.Response.Body)) {
				t.Errorf("%s: %d %s", c.ID, e.HTTPStatus(), got)
			}
		case c.Transport == "grpc":
			ce := MissingDatabase(db).Connect()
			if int(ce.Code()) != c.Error.Code || ce.Message() != c.Error.Message || len(ce.Details()) != 0 {
				t.Errorf("%s: gRPC %v %q %v", c.ID, ce.Code(), ce.Message(), ce.Details())
			}
			// The request binds to the missing database: its absence is core's answer, after binding.
			if prefix := strings.ReplaceAll(c.Request.ResourcePrefix, "{project}", corpusProject); prefix != "" {
				if got, err := BindName(prefix, Database{corpusProject, ""}); err != nil || got != db {
					t.Errorf("%s: resource prefix %q: %v %v", c.ID, prefix, got, err)
				}
			}
		default:
			t.Errorf("%s: transport %q", c.ID, c.Transport)
		}
	}
}

// TestIndexErrorCorpus maps every needs-index refusal of the 105-shape probe and the explain
// recording: FAILED_PRECONDITION, HTTP 400, the message as index wrote it, array-wrapped on
// runQuery and runAggregationQuery, no REST details; on gRPC code 9 and the console link as a
// google.rpc.Help.
func TestIndexErrorCorpus(t *testing.T) {
	var corpus struct {
		Cases []struct {
			ID          string `json:"id"`
			HTTP        int    `json:"http"`
			Message     string `json:"message"`
			BodyIsArray bool   `json:"bodyIsArray"`
			Links       []struct {
				Param   string `json:"param"`
				Payload string `json:"payload"`
			} `json:"links"`
		} `json:"cases"`
	}
	readCorpus(t, "indexes-105.json", &corpus)
	n := 0
	for _, c := range corpus.Cases {
		if c.HTTP == 200 {
			continue
		}
		n++
		ie := &index.Error{Message: c.Message}
		e := From(fmt.Errorf("query: %w", ie))
		if e.Code != CodeFailedPrecondition || e.HTTPStatus() != c.HTTP || !c.BodyIsArray || len(e.Details) != 0 {
			t.Errorf("%s: %v %d details %v", c.ID, e.Code, e.HTTPStatus(), e.Details)
		}
		want := `[{"error":{"code":400,"message":` + string(toJSON(t, c.Message)) + `,"status":"FAILED_PRECONDITION"}}]`
		if !sameJSON(t, e.RESTStreamBody(), []byte(want)) {
			t.Errorf("%s: %s", c.ID, e.RESTStreamBody())
		}
		ce := e.Connect()
		if ce.Code() != connect.Code(index.GRPCCode) || ce.Message() != c.Message || len(ce.Details()) != 1 {
			t.Fatalf("%s: gRPC %v %q %d details", c.ID, ce.Code(), ce.Message(), len(ce.Details()))
		}
		d, err := ce.Details()[0].Value()
		help, ok := d.(*errdetails.Help)
		if err != nil || !ok || len(help.GetLinks()) != 1 {
			t.Fatalf("%s: detail %v %v", c.ID, d, err)
		}
		var params []string
		for _, l := range c.Links {
			params = append(params, l.Param+"="+l.Payload)
		}
		link := "https://console.firebase.google.com/v1/r/project/" + corpusProject + "/firestore/indexes?" + strings.Join(params, "&")
		if help.GetLinks()[0].GetUrl() != link {
			t.Errorf("%s: link %q, want %q", c.ID, help.GetLinks()[0].GetUrl(), link)
		}
	}
	if n != 45 {
		t.Errorf("%d refusals, the probe recorded 45", n)
	}
}

func TestErrorForms(t *testing.T) {
	// HTTP statuses of Google's mapping, as the lite client reads them back.
	for code, status := range map[Code]int{CodeInvalidArgument: 400, CodeFailedPrecondition: 400, CodeOutOfRange: 400,
		CodeUnauthenticated: 401, CodePermissionDenied: 403, CodeNotFound: 404, CodeAborted: 409, CodeAlreadyExists: 409,
		CodeResourceExhausted: 429, CodeCanceled: 499, CodeUnknown: 500, CodeInternal: 500, CodeDataLoss: 500,
		CodeUnimplemented: 501, CodeUnavailable: 503, CodeDeadlineExceeded: 504} {
		if got := code.HTTPStatus(); got != status {
			t.Errorf("%v: HTTP %d, want %d", code, got, status)
		}
	}
	if Code(99).HTTPStatus() != 500 || Code(99).String() != "Code(99)" {
		t.Error("unknown code")
	}

	// The ABORTED and INVALID_ARGUMENT transaction texts (f0-errors.json).
	if b := TransactionExpired().RESTBody(); string(b) != `{"error":{"code":409,"message":"The referenced transaction has expired or is no longer valid.","status":"ABORTED"}}` {
		t.Errorf("expired: %s", b)
	}
	if b := InvalidTransaction().RESTStreamBody(); string(b) != `[{"error":{"code":400,"message":"Invalid transaction.","status":"INVALID_ARGUMENT"}}]` {
		t.Errorf("invalid: %s", b)
	}
	// The ErrorInfo of an index entry too large rides on both surfaces.
	if ce := IndexEntryTooLarge().Connect(); len(ce.Details()) != 1 || ce.Message() != "Index entry is too large." {
		t.Errorf("index entry: %v", ce)
	}
	// From.
	for _, c := range []struct {
		err  error
		code Code
		msg  string
	}{
		{context.Canceled, CodeCanceled, msgCanceled},
		{fmt.Errorf("x: %w", context.DeadlineExceeded), CodeDeadlineExceeded, msgDeadlineExceeded},
		{connect.NewError(connect.CodeUnavailable, errors.New("draining")), CodeUnavailable, "draining"},
		{errors.New("pq: secret detail"), CodeInternal, msgInternal},
		{fmt.Errorf("wrapped: %w", DocumentNotFound(root+"/c/m")), CodeNotFound, `Document "` + root + `/c/m" not found.`},
		{&TargetError{TargetID: 1, Err: PermissionDenied()}, CodePermissionDenied, "Missing or insufficient permissions."},
	} {
		if e := From(c.err); e.Code != c.code || e.Message != c.msg {
			t.Errorf("From(%v) = %v", c.err, e)
		}
	}
	if From(nil) != nil {
		t.Error("From(nil)")
	}
	if e := Unimplemented("Query Explain"); e.Code != CodeUnimplemented || e.HTTPStatus() != 501 ||
		e.Message != "Query Explain is not supported by this server" {
		t.Errorf("unimplemented: %v", e)
	}
}

func TestWriteREST(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteREST(rec, MissingDatabase(Database{"p", "nodb"}), true)
	if rec.Code != http.StatusNotFound || rec.Header().Get("Content-Type") != RESTContentType {
		t.Errorf("%d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	var body []map[string]map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body) != 1 || body[0]["error"]["status"] != "NOT_FOUND" ||
		!strings.HasSuffix(body[0]["error"]["message"].(string), "database. ") {
		t.Errorf("%s %v", rec.Body, err)
	}
	// Characters of console links are not escaped.
	rec = httptest.NewRecorder()
	WriteREST(rec, &index.Error{Message: "here: https://x/?a=1&b=<2>"}, false)
	if !strings.Contains(rec.Body.String(), "a=1&b=<2>") {
		t.Errorf("escaped: %s", rec.Body)
	}
}
