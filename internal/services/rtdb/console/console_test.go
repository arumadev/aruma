package console

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/osama300/aurma-project/internal/platform/apierr"
	"github.com/osama300/aurma-project/internal/platform/authn"
	"github.com/osama300/aurma-project/internal/platform/consolemount"
	"github.com/osama300/aurma-project/internal/platform/httpx"
	"github.com/osama300/aurma-project/internal/services/rtdb/core"
	"github.com/osama300/aurma-project/internal/store/storetest"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

// projects is the control plane's side of the database: project ids to numbers.
type projects map[string]int64

func (p projects) ProjectNumber(_ context.Context, id string) (int64, error) {
	if n, ok := p[id]; ok {
		return n, nil
	}
	return 0, apierr.New(apierr.NotFound, "Requested entity was not found.")
}

var testProjects = projects{"chat-app": 482915736204, "other-app": 582915736204}

// owners says which projects each account owns; the X-Account header names the caller, as
// the console's authentication would.
var owners = map[string][]string{"amal": {"chat-app"}, "badr": {"other-app"}}

// mount stands in for the console API: it routes, resolves {project} for the caller as the
// ownership gate does (NOT_FOUND for anything not owned), and writes errors.
func mount(t *testing.T, svc *core.Service) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	m := &consolemount.Mount{
		Handle: func(p authn.Policy, method, pattern string, h http.HandlerFunc) {
			if p != anyCred {
				t.Errorf("%s %s: policy %+v, want session or CLI token", method, pattern, p)
			}
			mux.HandleFunc(method+" "+pattern, h)
		},
		Project: func(r *http.Request) (consolemount.ProjectRef, error) {
			id := r.PathValue("project")
			for _, owned := range owners[r.Header.Get("X-Account")] {
				if owned == id {
					return consolemount.ProjectRef{Number: testProjects[id], ID: id}, nil
				}
			}
			return consolemount.ProjectRef{}, apierr.New(apierr.NotFound, "Requested entity was not found.")
		},
		Fail: func(w http.ResponseWriter, r *http.Request, err error) { httpx.WriteError(w, r, discard, err) },
	}
	Routes(svc, Options{DatabaseURL: func(id string) string { return "https://" + id + "-default-rtdb.rtdb.aruma.test" }})(m)
	return mux
}

func newService(t *testing.T) *core.Service {
	t.Helper()
	svc := core.New(core.NewPGStore(storetest.NewPool(t)), testProjects, core.Options{}, discard)
	t.Cleanup(func() { svc.Close(context.Background()) })
	return svc
}

type response struct {
	code int
	body []byte
	json map[string]any
	head http.Header
}

func call(t *testing.T, h http.Handler, account, method, path string, body any) response {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Account", account)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	out := response{code: rec.Code, body: rec.Body.Bytes(), head: rec.Header()}
	_ = json.Unmarshal(out.body, &out.json)
	return out
}

func (r response) status() string {
	if e, ok := r.json["error"].(map[string]any); ok {
		return e["status"].(string)
	}
	return ""
}

const base = "/console/v1/projects/chat-app/database"

func data(path string, extra ...string) string {
	q := url.Values{"path": {path}}
	for i := 0; i+1 < len(extra); i += 2 {
		q.Set(extra[i], extra[i+1])
	}
	return base + "/data?" + q.Encode()
}

func keys(t *testing.T, r response) []string {
	t.Helper()
	var out []string
	for _, c := range r.json["children"].([]any) {
		out = append(out, c.(map[string]any)["key"].(string))
	}
	return out
}

func TestDatabaseInfo(t *testing.T) {
	h := mount(t, newService(t))
	r := call(t, h, "amal", "GET", base, nil)
	if r.code != 200 || r.json["databaseURL"] != "https://chat-app-default-rtdb.rtdb.aruma.test" || r.json["enabled"] != true ||
		r.json["name"] != "chat-app-default-rtdb" || r.json["writeCount"] != float64(0) {
		t.Fatalf("info: %d %s", r.code, r.body)
	}
	if lim := r.json["limits"].(map[string]any); lim["keyBytes"] != float64(768) || lim["depth"] != float64(32) || lim["writeBytes"] != float64(1<<20) {
		t.Errorf("limits: %s", r.body)
	}
	call(t, h, "amal", "PUT", data("/a"), map[string]any{"value": map[string]any{"b": 1, "c": "x"}})
	r = call(t, h, "amal", "GET", base, nil)
	if size := r.json["size"].(map[string]any); size["rows"] != float64(2) || size["capped"] != false || r.json["writeCount"] != float64(1) {
		t.Errorf("after a write: %s", r.body)
	}

	// Disabled, the routes stay: the info says so and the rest refuse.
	off := mount(t, nil)
	r = call(t, off, "amal", "GET", base, nil)
	if r.code != 200 || r.json["enabled"] != false || r.json["databaseURL"] == "" {
		t.Fatalf("info while disabled: %d %s", r.code, r.body)
	}
	for _, c := range []struct{ method, path string }{{"GET", data("/")}, {"GET", base + "/rules"}, {"DELETE", data("/a")}} {
		if r := call(t, off, "amal", c.method, c.path, nil); r.code != 400 || r.status() != "FAILED_PRECONDITION" {
			t.Errorf("%s %s while disabled: %d %s", c.method, c.path, r.code, r.body)
		}
	}
}

func TestBrowse(t *testing.T) {
	h := mount(t, newService(t))
	long := strings.Repeat("ب", 600) // 1200 bytes
	ok(t, call(t, h, "amal", "PUT", data("/"), map[string]any{"value": map[string]any{
		"users": map[string]any{"amal": map[string]any{"name": "أمل", "age": 30}, "badr": true},
		"list":  map[string]any{"10": 1, "9": 2, "a": 3, "b": map[string]any{"c": 1}},
		"long":  long,
	}}))

	r := call(t, h, "amal", "GET", data("/"), nil)
	if r.code != 200 || r.json["path"] != "/" || r.json["exists"] != true || r.json["nextPageToken"] != "" {
		t.Fatalf("root: %d %s", r.code, r.body)
	}
	if got := strings.Join(keys(t, r), ","); got != "list,long,users" {
		t.Errorf("root keys %s", got)
	}
	children := r.json["children"].([]any)
	if c := children[0].(map[string]any); c["hasChildren"] != true || c["value"] != nil {
		t.Errorf("an object child: %v", c)
	}
	// A long string is cut among its siblings and whole at its own path.
	if c := children[1].(map[string]any); c["truncated"] != true || len(c["value"].(string)) != 1000 {
		t.Errorf("a long leaf: %v", c)
	}
	if r := call(t, h, "amal", "GET", data("/long"), nil); r.json["value"] != long || len(r.json["children"].([]any)) != 0 {
		t.Errorf("a leaf's own path: %s", r.body[:80])
	}

	// One page holding every child is in client order; pages follow byte order.
	r = call(t, h, "amal", "GET", data("/list"), nil)
	if got := strings.Join(keys(t, r), ","); got != "9,10,a,b" {
		t.Errorf("whole page %s", got)
	}
	var paged []string
	token := ""
	for range 10 {
		r := call(t, h, "amal", "GET", data("list", "pageSize", "3", "pageToken", token), nil)
		if r.code != 200 {
			t.Fatalf("page: %d %s", r.code, r.body)
		}
		paged = append(paged, keys(t, r)...)
		if token = r.json["nextPageToken"].(string); token == "" {
			break
		}
	}
	if got := strings.Join(paged, ","); got != "10,9,a,b" {
		t.Errorf("paged %s", got)
	}

	if r := call(t, h, "amal", "GET", data("/missing"), nil); r.code != 200 || r.json["exists"] != false || r.json["value"] != nil {
		t.Errorf("missing: %s", r.body)
	}
	for _, bad := range []string{data("/a.b"), data("/", "pageToken", "!!")} {
		if r := call(t, h, "amal", "GET", bad, nil); r.code != 400 || r.status() != "INVALID_ARGUMENT" {
			t.Errorf("GET %s: %d %s", bad, r.code, r.body)
		}
	}
}

func ok(t *testing.T, r response) response {
	t.Helper()
	if r.code/100 != 2 {
		t.Fatalf("%d %s", r.code, r.body)
	}
	return r
}

func TestWrites(t *testing.T) {
	svc := newService(t)
	h := mount(t, svc)

	// The writes go through the writer: a session listening sees each one.
	sink := &sink{}
	s, err := svc.Open(core.Target{Name: "chat-app-default-rtdb", ProjectID: "chat-app", Project: 482915736204}, core.Auth{Admin: true}, core.OpenOptions{Sink: sink})
	if err != nil {
		t.Fatal(err)
	}
	listening := make(chan error, 1)
	s.Listen("/rooms", nil, 0, "", func(_ []string, err error) { listening <- err })
	if err := <-listening; err != nil {
		t.Fatal(err)
	}

	if r := call(t, h, "amal", "PUT", data("/rooms/r1"), map[string]any{"value": map[string]any{"title": "عام", "at": map[string]any{".sv": "timestamp"}}}); r.code != 204 {
		t.Fatalf("set: %d %s", r.code, r.body)
	}
	ok(t, call(t, h, "amal", "PATCH", data("/rooms"), map[string]any{"value": map[string]any{"r1/title": "General", "r2/title": "Two"}}))
	ok(t, call(t, h, "amal", "DELETE", data("/rooms/r2"), nil))
	s.Close()
	// One push per write: the set, the update (several children at once), the delete.
	if got := strings.Join(sink.paths(), " "); got != "rooms/r1 rooms rooms/r2" {
		t.Errorf("pushes to the listener: %q", got)
	}

	r := call(t, h, "amal", "GET", data("/rooms/r1"), nil)
	if got := strings.Join(keys(t, r), ","); got != "at,title" || r.json["children"].([]any)[1].(map[string]any)["value"] != "General" {
		t.Errorf("after the writes: %s", r.body)
	}
	if r := call(t, h, "amal", "GET", data("/rooms/r2"), nil); r.json["exists"] != false {
		t.Errorf("deleted node: %s", r.body)
	}

	// The data is checked as an SDK's would be.
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"PUT", data("/x"), map[string]any{"value": map[string]any{"a.b": 1}}},
		{"PUT", data("/x"), map[string]any{}},
		{"PUT", data("/x$"), map[string]any{"value": 1}},
		{"PATCH", data("/x"), map[string]any{"value": map[string]any{"a": 1, "a/b": 2}}},
		{"PUT", data("/x"), map[string]any{"value": 1, "extra": true}},
	} {
		if r := call(t, h, "amal", c.method, c.path, c.body); r.code != 400 || r.status() != "INVALID_ARGUMENT" {
			t.Errorf("%s %s %v: %d %s", c.method, c.path, c.body, r.code, r.body)
		}
	}
}

// sink records the paths of the pushes a session receives.
type sink struct {
	mu     sync.Mutex
	pushes []core.Push
}

func (s *sink) Push(p core.Push) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pushes = append(s.pushes, p)
}

func (s *sink) paths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, p := range s.pushes {
		out = append(out, p.Path.String())
	}
	return out
}

func TestExport(t *testing.T) {
	h := mount(t, newService(t))
	ok(t, call(t, h, "amal", "PUT", data("/"), map[string]any{"value": map[string]any{"a": map[string]any{"b": 1, "c": map[string]any{".value": "x", ".priority": 2}}, "d": true}}))
	r := call(t, h, "amal", "GET", base+"/data:export?path=/a", nil)
	if r.code != 200 || string(r.body) != `{"b":1,"c":"x"}` || !strings.Contains(r.head.Get("Content-Disposition"), "chat-app-default-rtdb-a-export.json") {
		t.Fatalf("export: %d %s %v", r.code, r.body, r.head)
	}
	if r := call(t, h, "amal", "GET", base+"/data:export?path=/missing", nil); r.code != 200 || string(r.body) != "null" {
		t.Errorf("export of nothing: %s", r.body)
	}

	defer func(rows int) { maxExportRows = rows }(maxExportRows)
	maxExportRows = 2
	if r := call(t, h, "amal", "GET", base+"/data:export?path=/", nil); r.code != 400 || r.status() != "FAILED_PRECONDITION" {
		t.Errorf("export over the cap: %d %s", r.code, r.body)
	}
	if r := call(t, h, "amal", "GET", base+"/data:export?path=/a", nil); r.code != 200 {
		t.Errorf("export at the cap: %d %s", r.code, r.body)
	}
}

func TestRules(t *testing.T) {
	h := mount(t, newService(t))
	r := call(t, h, "amal", "GET", base+"/rules", nil)
	if r.code != 200 || r.json["rules"] != core.LockedRules {
		t.Fatalf("a new database's rules: %s", r.body)
	}

	// Every error comes with its line and column.
	bad := "{\n  \"rules\": {\n    \".read\": \"auth != null &&\",\n    \".write\": \"nope()\"\n  }\n}"
	r = call(t, h, "amal", "PUT", base+"/rules", map[string]any{"rules": bad})
	if r.code != 400 || r.status() != "INVALID_ARGUMENT" {
		t.Fatalf("invalid rules: %d %s", r.code, r.body)
	}
	var detail struct {
		Error struct {
			Details []struct {
				Type            string `json:"@type"`
				FieldViolations []apierr.FieldViolation
				Errors          []ruleError
			}
		}
	}
	_ = json.Unmarshal(r.body, &detail)
	var list []ruleError
	field := ""
	for _, d := range detail.Error.Details {
		switch d.Type {
		case rulesErrorType:
			list = d.Errors
		case "type.googleapis.com/google.rpc.BadRequest":
			field = d.FieldViolations[0].Field
		}
	}
	if field != "rules" || len(list) != 2 || list[0].Line != 3 || list[1].Line != 4 || list[0].Column < 1 || list[1].Path != "rules/.write" {
		t.Fatalf("errors: %+v (%s)", list, r.body)
	}
	if got := call(t, h, "amal", "GET", base+"/rules", nil); got.json["rules"] != core.LockedRules {
		t.Error("invalid rules were saved")
	}
	malformed := call(t, h, "amal", "PUT", base+"/rules", map[string]any{"rules": "{\"rules\": {"})
	if malformed.code != 400 || !strings.Contains(string(malformed.body), `"line":1`) {
		t.Errorf("malformed document: %s", malformed.body)
	}

	// Valid rules are saved as written, comments and all.
	good := "{\n  // signed-in users only\n  \"rules\": {\".read\": \"auth != null\", \".write\": false}\n}"
	if r := call(t, h, "amal", "PUT", base+"/rules", map[string]any{"rules": good}); r.code != 200 || r.json["rules"] != good {
		t.Fatalf("save: %d %s", r.code, r.body)
	}
	if r := call(t, h, "amal", "GET", base+"/rules", nil); r.json["rules"] != good {
		t.Errorf("saved: %s", r.body)
	}
}

// A caller reaches only the databases of projects they own: anything else is NOT_FOUND, as
// for every project route, whether the project exists or not.
func TestTenantIsolation(t *testing.T) {
	h := mount(t, newService(t))
	ok(t, call(t, h, "amal", "PUT", data("/secret"), map[string]any{"value": "a's data"}))
	other := "/console/v1/projects/chat-app/database"
	for _, c := range []struct{ method, path string }{
		{"GET", other}, {"GET", other + "/data?path=/"}, {"GET", other + "/data:export?path=/"},
		{"PUT", other + "/data?path=/secret"}, {"PATCH", other + "/data?path=/"}, {"DELETE", other + "/data?path=/secret"},
		{"GET", other + "/rules"}, {"PUT", other + "/rules"},
		{"GET", "/console/v1/projects/missing-app/database/data?path=/"},
	} {
		var body any
		switch {
		case strings.HasSuffix(c.path, "/rules") && c.method == "PUT":
			body = map[string]any{"rules": `{"rules": {".read": true}}`}
		case c.method == "PUT" || c.method == "PATCH":
			body = map[string]any{"value": map[string]any{"secret": "b's"}}
		}
		if r := call(t, h, "badr", c.method, c.path, body); r.code != 404 || r.status() != "NOT_FOUND" {
			t.Errorf("badr %s %s: %d %s", c.method, c.path, r.code, r.body)
		}
	}
	if r := call(t, h, "amal", "GET", data("/secret"), nil); r.json["value"] != "a's data" {
		t.Errorf("a's data after b's attempts: %s", r.body)
	}
	// b's own database is separate.
	r := call(t, h, "badr", "GET", "/console/v1/projects/other-app/database/data?path=/", nil)
	if r.code != 200 || r.json["exists"] != false {
		t.Errorf("b's database: %s", r.body)
	}
}

func TestErrorMapping(t *testing.T) {
	for _, c := range []struct {
		err  error
		code apierr.Code
	}{
		{core.ErrInvalidPath, apierr.InvalidArgument},
		{core.ErrTooBig, apierr.InvalidArgument},
		{core.ErrNoDatabase, apierr.NotFound},
		{core.ErrDataStale, apierr.Aborted},
		{core.ErrWriteRate, apierr.ResourceExhausted},
		{core.ErrTooManyConnections, apierr.ResourceExhausted},
		{core.ErrPermissionDenied, apierr.PermissionDenied},
		{core.ErrUnavailable, apierr.Unavailable},
	} {
		if got := apierr.CodeOf(rtdbError(c.err)); got != c.code {
			t.Errorf("%v: %v, want %v", c.err, got, c.code)
		}
	}
	plain := errors.New("x")
	if rtdbError(plain) != plain {
		t.Error("other errors pass through")
	}
}
