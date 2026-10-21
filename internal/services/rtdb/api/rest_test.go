package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/arumadev/aruma/internal/services/rtdb/core"
)

func TestPretty(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		// The emulator's print=pretty and its errors (rest.golden.json).
		{`{"error":"Permission denied"}`, "{\n  \"error\" : \"Permission denied\"\n}"},
		{`{"x":{".priority":1,"y":2},"z":true}`, "{\n  \"x\" : {\n    \".priority\" : 1,\n    \"y\" : 2\n  },\n  \"z\" : true\n}"},
		{`["a","b"]`, `[ "a", "b" ]`},
		{`{"a":[1,{"b":null}]}`, "{\n  \"a\" : [ 1, {\n    \"b\" : null\n  } ]\n}"},
		{`"a:{,}[\"]"`, `"a:{,}[\"]"`},
		{`{}`, `{ }`},
		{`5`, `5`},
	} {
		if got := string(pretty([]byte(c.in))); got != c.want {
			t.Errorf("pretty(%s) =\n%s\nwant\n%s", c.in, got, c.want)
		}
	}
}

func TestParseQuery(t *testing.T) {
	str := func(s string) *string { return &s }
	no := false
	for _, c := range []struct {
		query string
		want  *core.QuerySpec
		err   string
	}{
		{"", nil, ""},
		{`orderBy="$key"&limitToFirst=2`, &core.QuerySpec{Index: ".key", Limit: 2}, ""},
		{`orderBy="$value"&limitToLast=3`, &core.QuerySpec{Index: ".value", Limit: 3, FromRight: true}, ""},
		{`orderBy="$priority"&startAt=1&endAt="b"`, &core.QuerySpec{Index: ".priority", StartValue: json.RawMessage(`1`), EndValue: json.RawMessage(`"b"`)}, ""},
		{`orderBy="a/b"&equalTo=true`, &core.QuerySpec{Index: "a/b", StartValue: json.RawMessage(`true`), EndValue: json.RawMessage(`true`)}, ""},
		{`orderBy="s"&startAfter=1&endBefore=5`, &core.QuerySpec{Index: "s", StartValue: json.RawMessage(`1`), StartName: str(core.MaxName),
			StartInclusive: &no, EndValue: json.RawMessage(`5`), EndName: str(core.MinName), EndInclusive: &no}, ""},
		// The emulator's texts.
		{`limitToFirst=1`, nil, "orderBy must be defined when other query parameters are defined"},
		{`orderBy=s`, nil, "orderBy must be a valid JSON encoded path"},
		{`orderBy="$bogus"`, nil, "orderBy must be a valid JSON encoded path"},
		{`orderBy="a.b"`, nil, "orderBy must be a valid JSON encoded path"},
		{`orderBy="$key"&limitToFirst=-1`, nil, "limitToFirst must be positive"},
		{`orderBy="$key"&limitToLast=x`, nil, "limitToLast must be an integer"},
		{`orderBy="$key"&limitToFirst=1&limitToLast=1`, nil, "Only one of limitToFirst and limitToLast may be specified"},
		{`orderBy="$key"&equalTo="b"&startAt="a"`, nil, "equalTo cannot be specified in addition to startAfter, startAt, endAt, or endBefore"},
		{`orderBy="$key"&startAt=1`, nil, "Provided key index type is invalid, must be string"},
		{`orderBy="$key"&startAt=b`, nil, "Constraint key field must be a valid key name"},
	} {
		q, _ := url.ParseQuery(c.query)
		got, err := parseQuery(q)
		switch {
		case c.err != "":
			if err == nil || err.Error() != c.err {
				t.Errorf("%s: %v, want %q", c.query, err, c.err)
			}
		case err != nil:
			t.Errorf("%s: %v", c.query, err)
		default:
			g, _ := json.Marshal(got)
			w, _ := json.Marshal(c.want)
			if string(g) != string(w) {
				t.Errorf("%s: %s, want %s", c.query, g, w)
			}
		}
	}
}

func TestParseTimeout(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"3s": 3 * time.Second, "2seconds": 2 * time.Second, "3min": 3 * time.Minute, "500ms": 500 * time.Millisecond,
		"15min": 15 * time.Minute, "16min": -1, "900001ms": -1, "bogus": -1, "3": -1, "-1s": -1,
	} {
		got, err := parseTimeout(in)
		if want < 0 && err == nil || want >= 0 && (err != nil || got != want) {
			t.Errorf("%s: %v %v, want %v", in, got, err, want)
		}
	}
}

func TestReadREST(t *testing.T) {
	for _, c := range []struct {
		method, target string
		header         map[string]string
		err            string
		check          func(*restRequest) bool
	}{
		{"POST", "/a.json", map[string]string{"X-HTTP-Method-Override": "patch"}, "", func(r *restRequest) bool { return r.method == "PATCH" }},
		{"POST", "/a.json?x-http-method-override=DELETE", nil, "", func(r *restRequest) bool { return r.method == "DELETE" }},
		{"POST", "/a.json", map[string]string{"X-HTTP-Method-Override": "BREW"}, "Unsupported request type", nil},
		{"HEAD", "/a.json", nil, "Unsupported request type", nil},
		{"GET", "/a.json?format=xml", nil, "Export format must be export if present", nil},
		{"GET", "/a.json?shallow=true&limitToLast=2", nil, "", func(r *restRequest) bool { return r.shallow && r.shallowLimit == -2 && r.query == nil }},
		{"GET", `/a.json?shallow=true&orderBy="$key"`, nil, "orderBy not supported for with shallow GET, keys are returned in lexicographical order", nil},
		{"PUT", `/a.json?orderBy="$key"`, nil, "Querying related parameters not supported on this request type", nil},
		{"PATCH", "/a.json", map[string]string{"X-Firebase-ETag": "true"}, "The 'X-Firebase-ETag' header is not supported with PATCH requests", nil},
		{"GET", "/a.json?shallow=true", map[string]string{"X-Firebase-ETag": "true"}, "Mixing 'shallow' and etag requests is not supported", nil},
		{"POST", "/a.json", map[string]string{"If-Match": "x"}, "The 'if-match' header is not supported with GET, PATCH or POST requests", nil},
		{"DELETE", "/a.json", map[string]string{"If-None-Match": "x"}, "The 'if-none-match' header is not supported with PUT, POST, PATCH or DELETE requests", nil},
		{"PUT", "/a.json?print=silent", map[string]string{"If-Match": "x"}, "Mixing 'shallow', querying parameters or 'print=silent' and if-match or if-none-match requests is not supported", nil},
		{"PUT", "/a.json?writeSizeLimit=huge", nil, "writeSizeLimit should be one of {tiny, large, small, medium, unlimited}", nil}, // production's order; the emulator's is {medium, tiny, large, unlimited, small}
		{"GET", "/a.json?timeout=1h", nil, "timeout must contain length and unit (s/seconds/min/minutes), such as 1s, 2seconds, or 3min", nil},
		{"GET", "/a.json", map[string]string{"Accept": "text/event-stream"}, errStreaming.msg, nil},
		{"PUT", "/a.json", map[string]string{"If-Match": "", "X-Firebase-ETag": "TRUE"}, "", func(r *restRequest) bool { return r.ifMatch != nil && *r.ifMatch == "" && r.etag }},
	} {
		r := httptest.NewRequest(c.method, c.target, nil)
		for k, v := range c.header {
			r.Header.Set(k, v)
		}
		req, err := readREST(r)
		switch {
		case c.err != "":
			if err == nil || err.Error() != c.err {
				t.Errorf("%s %s: %v, want %q", c.method, c.target, err, c.err)
			}
		case err != nil:
			t.Errorf("%s %s: %v", c.method, c.target, err)
		case !c.check(req):
			t.Errorf("%s %s: %+v", c.method, c.target, req)
		}
	}
}

func TestEchoUpdate(t *testing.T) {
	merge := []core.Child{
		{Path: core.Path{"a", "b"}, Value: core.Int(1)},
		{Path: core.Path{"a", "c"}, Value: nil},
		{Path: core.Path{"t"}, Value: core.Int(5)},
	}
	if got, want := string(echoUpdate(merge)), `{"a":{"b":1,"c":null},"t":5}`; got != want {
		t.Errorf("echo %s, want %s", got, want)
	}
}

// Over REST the database's rules apply as for any client: a new database is locked, and the
// REST API answers as Firebase's, through the same writer as the protocol.
func TestRESTLockedAndOpen(t *testing.T) {
	a, _, _ := newTestAPI(t, Options{}, core.Options{})
	do := func(method, target, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		a.ServeHTTP(rec, httptest.NewRequest(method, target, strings.NewReader(body)))
		return rec
	}
	if rec := do("PUT", "/x.json?ns=chat-app-default-rtdb", "1"); rec.Code != http.StatusUnauthorized ||
		rec.Body.String() != "{\n  \"error\" : \"Permission denied\"\n}\n" || rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("locked: %d %q %v", rec.Code, rec.Body, rec.Header())
	}
	if rec := do("OPTIONS", "/x.json?ns=chat-app-default-rtdb", ""); rec.Code != 200 || rec.Header().Get("Allow") != restMethods {
		t.Errorf("OPTIONS: %d %v", rec.Code, rec.Header())
	}
}
