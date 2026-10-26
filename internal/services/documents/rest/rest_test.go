package rest_test

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/arumadev/aruma/internal/platform/httpx"
	"github.com/arumadev/aruma/internal/services/documents/api/apitest"
	"github.com/arumadev/aruma/internal/services/documents/rest"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// The REST surface, in process: plain HTTP against the handler under Common's body cap, the
// service layer and core on Postgres.

const root = "projects/" + apitest.Project + "/databases/(default)/documents"

type harness struct {
	env *apitest.Env
	url string
}

func newHarness(t *testing.T, o apitest.Options) *harness {
	t.Helper()
	env := apitest.New(t, o)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ts := httptest.NewServer(httpx.CommonWithLimit(rest.New(rest.Options{Service: env.Service, Logger: log}), log, nil, 17<<20))
	t.Cleanup(ts.Close)
	return &harness{env: env, url: ts.URL}
}

// do sends a request; token "" sends none.
func (h *harness) do(t *testing.T, method, path, body, token string, header ...string) (int, http.Header, []byte) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, h.url+path, r)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, b
}

// corpusCase is a recorded case, as test/oracle/documents/corpus holds it.
type corpusCase struct {
	ID        string `json:"id"`
	Transport string `json:"transport"`
	Request   struct {
		Method string          `json:"method"`
		Path   string          `json:"path"`
		Body   json.RawMessage `json:"body"`
		Auth   string          `json:"auth"`
	} `json:"request"`
	Response struct {
		Body json.RawMessage `json:"body"`
		HTTP int             `json:"http"`
	} `json:"response"`
}

type corpusFile struct {
	Setup struct {
		Commits []struct {
			Writes []struct {
				Update *struct {
					Name   string          `json:"name"`
					Fields json.RawMessage `json:"fields"`
				} `json:"update"`
			} `json:"writes"`
		} `json:"commits"`
	} `json:"setup"`
	Cases []corpusCase `json:"cases"`
}

// notReplayed are the recorded REST cases whose answer this phase does not reproduce, each with
// its reason. A case listed here that does reproduce fails the test, so the list stays true.
var notReplayed = map[string]string{
	// The documents these delete or read were written by :commit calls of the recorder that are
	// not cases (Commit is F2's).
	"f1-delete.json rest.exists_false_on_existing": "state from an unrecorded commit",
	"f1-delete.json rest.exists_true":              "state from an unrecorded commit",
	"f1-delete.json rest.update_time_stale":        "state from an unrecorded commit",
}

// unserved are the custom methods of later phases: their cases are not sent.
var unserved = []string{":runQuery", ":runAggregationQuery", ":partitionQuery"}

// beginCases are the recorded BeginTransaction cases whose transaction later cases of the same
// file name by a placeholder.
var beginCases = map[string]string{
	"rest.txn.begin_read_only":           "<the read-only transaction>",
	"rest.txn.begin_read_write":          "<the read-write transaction>",
	"rest.txn.begin_read_only_read_time": "<the read-only transaction at the setup commitTime>",
}

// reordered are the recorded requests whose members the recorder sent in another order than the
// recording keeps (sorted): a oneof's error names the member read second.
var reordered = map[string]string{
	"f1-batch-get.json rest.txn.and_new_transaction": `{"documents":["projects/{project}/databases/(default)/documents/aruma-ref-f1-get/g1"],` +
		`"transaction":"<the read-only transaction>","newTransaction":{"readOnly":{}}}`,
	"f1-batch-get.json rest.txn.and_read_time": `{"documents":["projects/{project}/databases/(default)/documents/aruma-ref-f1-get/g1"],` +
		`"transaction":"<the read-only transaction>","readTime":"<readTime: RFC 3339>"}`,
}

// TestCorpusReplay replays the F1 recordings' REST cases in their order on one database: the
// setup commits as updates, then every case whose request carries no placeholder but the
// transactions begun by earlier cases and the setup's commit time, and that needs no later phase.
// Its status and body must be production's, the times, page tokens and transaction ids
// normalised as the recorder normalised them. The transactions a file began are rolled back after
// it, so that the read-write one's locks do not hold the next file's setup.
func TestCorpusReplay(t *testing.T) {
	h := newHarness(t, apitest.Options{})
	tok := h.env.JWT(t)
	dir := filepath.Join("..", "..", "..", "..", "test", "oracle", "documents", "corpus")
	compared, skipped := 0, 0
	reproduced := map[string]bool{}
	for _, name := range []string{"f1-get.json", "f1-batch-get.json", "f1-create.json", "f1-update.json", "f1-delete.json",
		"f1-list-documents.json", "f1-list-collection-ids.json", "f1-resource-names.json", "f1-write-results.json"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		var f corpusFile
		if err := json.Unmarshal(b, &f); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var setupTime string
		for _, c := range f.Setup.Commits {
			for _, w := range c.Writes {
				if w.Update == nil {
					t.Fatalf("%s: a setup write that is not an update", name)
				}
				path := "/v1/" + strings.ReplaceAll(w.Update.Name, "{project}", apitest.Project)
				code, _, body := h.do(t, http.MethodPatch, path, `{"fields":`+string(w.Update.Fields)+`}`, tok)
				if code != 200 {
					t.Fatalf("%s setup %s: %d %s", name, path, code, body)
				}
				var d struct {
					UpdateTime string `json:"updateTime"`
				}
				if err := json.Unmarshal(body, &d); err != nil {
					t.Fatal(err)
				}
				setupTime = d.UpdateTime
			}
		}
		txns := map[string]string{} // placeholder: id
		for _, c := range f.Cases {
			path, body := c.Request.Path, string(c.Request.Body)
			if b, ok := reordered[name+" "+c.ID]; ok {
				body = b
			}
			for placeholder, id := range txns {
				path = strings.ReplaceAll(path, placeholder, url.QueryEscape(id))
				body = strings.ReplaceAll(body, placeholder, id)
			}
			if setupTime != "" && strings.Contains(c.ID, "txn.") {
				path = strings.ReplaceAll(path, "<readTime: the setup commitTime>", setupTime)
				body = strings.ReplaceAll(body, "<readTime: RFC 3339>", setupTime)
			}
			if c.Transport != "rest" || strings.Contains(path, "<") || strings.Contains(body, "<") ||
				slices.ContainsFunc(unserved, func(v string) bool { return strings.Contains(path, v) }) {
				skipped++
				continue
			}
			token := tok
			if c.Request.Auth == "none" {
				token = ""
			}
			path = "/v1/" + strings.ReplaceAll(path, "{project}", apitest.Project)
			body = strings.ReplaceAll(body, "{project}", apitest.Project)
			code, _, got := h.do(t, c.Request.Method, path, body, token)
			if id := transactionOf(got); id != "" {
				if placeholder, ok := beginCases[c.ID]; ok {
					txns[placeholder] = id
				} else {
					txns["<begun by "+c.ID+">"] = id
				}
			}
			want := string(c.Response.Body)
			if strings.Contains(want, "<auto id") {
				skipped++
				continue
			}
			compared++
			ok := code == c.Response.HTTP && sameJSON(t, normalise(t, got), normalise(t, []byte(want)))
			if reason, known := notReplayed[name+" "+c.ID]; known {
				if ok {
					t.Errorf("%s %s: reproduced, but listed as not (%s)", name, c.ID, reason)
				}
				reproduced[name+" "+c.ID] = true
				continue
			}
			if !ok {
				t.Errorf("%s %s: %s %s\n got %d %s\nwant %d %s", name, c.ID, c.Request.Method, path, code, got, c.Response.HTTP, want)
			}
		}
		for _, id := range txns {
			h.do(t, http.MethodPost, "/v1/"+root+":rollback", `{"transaction":"`+id+`"}`, tok)
		}
	}
	for _, id := range slices.Sorted(maps.Keys(notReplayed)) {
		if !reproduced[id] {
			t.Errorf("%s is listed as not replayed but was not sent", id)
		}
	}
	t.Logf("compared %d recorded REST cases, skipped %d", compared, skipped)
	if compared < 80 {
		t.Errorf("only %d cases compared", compared)
	}
}

// transactionOf is the transaction id an answer carries: BeginTransaction's, or a batchGet's with
// new_transaction, in its first element.
func transactionOf(body []byte) string {
	var res struct {
		Transaction string `json:"transaction"`
	}
	if json.Unmarshal(body, &res) != nil {
		var arr []json.RawMessage
		if json.Unmarshal(body, &arr) != nil || len(arr) == 0 || json.Unmarshal(arr[0], &res) != nil {
			return ""
		}
	}
	return res.Transaction
}

// requestTimeRE matches a REQUEST_TIME transform's result: whole milliseconds.
var requestTimeRE = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d{3})?Z$`)

// normalise replaces what the recorder replaced (lib.mjs): times and page tokens. A
// REQUEST_TIME result, which the recorder kept, becomes a placeholder when it has whole
// milliseconds, on both sides.
func normalise(t *testing.T, b []byte) []byte {
	t.Helper()
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return b
	}
	var walk func(any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			for k, e := range x {
				switch k {
				case "createTime", "updateTime", "readTime", "commitTime":
					if _, ok := e.(string); ok {
						x[k] = "<" + k + ": RFC 3339>"
						continue
					}
				case "nextPageToken":
					x[k] = "<nextPageToken>"
					continue
				case "transaction":
					if _, ok := e.(string); ok {
						x[k] = "<transaction>"
						continue
					}
				case "transformResults":
					for _, r := range e.([]any) {
						if r, ok := r.(map[string]any); ok {
							if ts, ok := r["timestampValue"].(string); ok && requestTimeRE.MatchString(ts) {
								r["timestampValue"] = "<request time: whole milliseconds>"
							}
						}
					}
				}
				x[k] = walk(e)
			}
		case []any:
			for i, e := range x {
				x[i] = walk(e)
			}
		}
		return v
	}
	out, err := json.Marshal(walk(v))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sameJSON(t *testing.T, a, b []byte) bool {
	t.Helper()
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return bytes.Equal(a, b)
	}
	return reflect.DeepEqual(x, y)
}

// The forms the replay cannot show: an error body byte for byte, the array-wrapped error of
// batchGet, a text/plain body, a missing database's REST text, and the Admin API closed.
func TestForms(t *testing.T) {
	h := newHarness(t, apitest.Options{})
	tok := h.env.JWT(t)
	code, hd, body := h.do(t, http.MethodGet, "/v1/"+root+"/c/nope", "", tok)
	if want := `{"error":{"code":404,"message":"Document \"` + root + `/c/nope\" not found.","status":"NOT_FOUND"}}`; code != 404 || string(body) != want {
		t.Errorf("missing document: %d %s, want %s", code, body, want)
	}
	if ct := hd.Get("Content-Type"); ct != "application/json; charset=UTF-8" {
		t.Errorf("Content-Type %q", ct)
	}
	code, _, body = h.do(t, http.MethodPost, "/v1/projects/"+apitest.Project+"/databases/(default)/documents:batchGet",
		`{"documents":["c/d"]}`, tok)
	if want := `[{"error":{"code":400,"message":"Document name \"c/d\" lacks \"projects\" at index 0.","status":"INVALID_ARGUMENT"}}]`; code != 400 || string(body) != want {
		t.Errorf("batchGet error: %d %s", code, body)
	}
	// The web SDK sends JSON as text/plain to avoid a preflight (ADR-027 §5.4).
	code, _, body = h.do(t, http.MethodPatch, "/v1/"+root+"/c/d", `{"fields":{"a":{"integerValue":"1"}}}`, tok, "Content-Type", "text/plain;charset=UTF-8")
	if code != 200 || !strings.Contains(string(body), `"a":{"integerValue":"1"}`) {
		t.Errorf("text/plain body: %d %s", code, body)
	}
	code, _, body = h.do(t, http.MethodGet, "/v1/projects/"+apitest.Project+"/databases/aruma-ref-nonexistent/documents/c/d", "", tok)
	if want := `{"error":{"code":404,"message":"The database aruma-ref-nonexistent does not exist for project ` + apitest.Project +
		` Please visit https://console.cloud.google.com/datastore/setup?project=` + apitest.Project +
		` to add a Cloud Datastore or Cloud Firestore database. ","status":"NOT_FOUND"}}`; code != 404 || string(body) != want {
		t.Errorf("missing database: %d %s\nwant %s", code, body, want)
	}
	// An id this server never issued (f0-errors.json rest.txn_unknown.commit).
	code, _, body = h.do(t, http.MethodPost, "/v1/projects/"+apitest.Project+"/databases/(default)/documents:commit", `{"transaction":"AAAA"}`, tok)
	if want := `{"error":{"code":400,"message":"Invalid transaction.","status":"INVALID_ARGUMENT"}}`; code != 400 || string(body) != want {
		t.Errorf(":commit in a transaction: %d %s", code, body)
	}
	code, hd, _ = h.do(t, http.MethodGet, "/v1/projects/"+apitest.Project+"/databases/(default)", "", tok, "Origin", "https://app.example")
	if code != http.StatusNotImplemented || hd.Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("Admin API: %d, CORS %q", code, hd.Get("Access-Control-Allow-Origin"))
	}
	// Before F5 only admins: an end user and an anonymous caller are refused as production
	// refuses a caller its rules deny.
	for _, token := range []string{"", apitest.IDToken(t)} {
		code, _, body = h.do(t, http.MethodGet, "/v1/"+root+"/c/d", "", token)
		if want := `{"error":{"code":403,"message":"Missing or insufficient permissions.","status":"PERMISSION_DENIED"}}`; code != 403 || string(body) != want {
			t.Errorf("caller %.10q: %d %s", token, code, body)
		}
	}
}

// The data paths are open to every origin, with ADR-027 §5.2's preflight.
func TestCORS(t *testing.T) {
	h := newHarness(t, apitest.Options{})
	code, hd, _ := h.do(t, http.MethodOptions, "/v1/"+root+"/c/d", "", "",
		"Origin", "https://app.example", "Access-Control-Request-Method", "PATCH",
		"Access-Control-Request-Headers", "authorization, x-goog-api-client")
	if code != http.StatusNoContent || hd.Get("Access-Control-Allow-Origin") != "*" ||
		hd.Get("Access-Control-Allow-Methods") != "GET, POST, PATCH, DELETE" ||
		!strings.Contains(hd.Get("Access-Control-Allow-Headers"), "google-cloud-resource-prefix") ||
		hd.Get("Access-Control-Allow-Credentials") != "" {
		t.Errorf("preflight: %d %v", code, hd)
	}
	_, hd, _ = h.do(t, http.MethodGet, "/v1/"+root+"/c/d", "", h.env.JWT(t), "Origin", "https://app.example")
	if hd.Get("Access-Control-Allow-Origin") != "*" || hd.Get("Access-Control-Expose-Headers") != "X-HTTP-Session-Id" {
		t.Errorf("data path: %v", hd)
	}
}

func TestOwns(t *testing.T) {
	for path, want := range map[string]bool{
		"/v1/projects/p/databases":                         true,
		"/v1/projects/p/databases/(default)":               true,
		"/v1/projects/p/databases/(default)/documents/c/d": true,
		"/google.firestore.v1.Firestore/Listen/channel":    true,
		"/v1/projects/p/rulesets":                          false,
		"/v1/projects/p/databasesx":                        false,
		"/v1/projects/p":                                   false,
		"/v1beta1/projects/p/databases":                    false,
	} {
		if got := rest.Owns(path); got != want {
			t.Errorf("Owns(%q) = %v", path, got)
		}
	}
}

// wireGaps are the cases of f1-wire-errors.json this server does not answer as production, each
// with its reason; all are outside F1. A listed case that is answered as production fails the
// test, so the list stays true.
var wireGaps = map[string]string{
	"path.no_database_segment":                  "Google's front end answers a path no API binds with its HTML 404 page; Aruma's router answers it",
	"path.project_id.Bad_Project!":              "Google's consumer-project check (ErrorInfo, LocalizedMessage, Help) runs before Firestore; Aruma refuses a project the caller may not use as production's rules do",
	"path.project_id.ab":                        "Google's API-enablement check for a project that is not a Google project",
	"path.project_id.UPPER-CASE":                "Google's consumer-project check",
	"path.project_id.aruma-ref-no-such-project": "Google's consumer-project check",
	"path.project_id.-starts-with-dash":         "Google's consumer-project check",
	"path.project_id.123456789012":              "Google's consumer-project check",
	"admin.name_without_projects":               "the Admin API is F3; Google's front end answers an unbound path with its HTML 404 page",
	"admin.name_other_prefix":                   "the Admin API is F3; Google's front end answers an unbound path with its HTML 404 page",
	"admin.database_get_bad_id":                 "the Admin API is F3",
	"admin.database_get_missing":                "the Admin API is F3",
	"json.bad_enum_number":                      "a query's meaning is checked by RunQuery, F3: this server answers it UNIMPLEMENTED",
	"value.no_type_in_filter":                   "a query's meaning is checked by RunQuery, F3: this server answers it UNIMPLEMENTED",
}

// TestWireErrorsReplay replays every REST case of f1-wire-errors.json: production's answers to
// bodies, query parameters and paths the front end refuses or reads leniently. The few requests
// the recorder could not keep (a time relative to the run, a transaction id) are rebuilt as
// recorder sent them, in the order it sent their members.
func TestWireErrorsReplay(t *testing.T) {
	h := newHarness(t, apitest.Options{})
	tok := h.env.JWT(t)
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "test", "oracle", "documents", "corpus", "f1-wire-errors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f corpusFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.Setup.Commits {
		for _, w := range c.Writes {
			path := "/v1/" + strings.ReplaceAll(w.Update.Name, "{project}", apitest.Project)
			if code, _, body := h.do(t, http.MethodPatch, path, `{"fields":`+string(w.Update.Fields)+`}`, tok); code != 200 {
				t.Fatalf("setup %s: %d %s", path, code, body)
			}
		}
	}
	g1 := root + "/aruma-ref-f1-get/g1"
	from := `{"from":[{"collectionId":"aruma-ref-f1-get"}]}`
	readTime := time.Now().Add(-5 * time.Second).UTC().Format(time.RFC3339)
	// A minute ago in production; here a moment ago, after the setup, at whole microseconds.
	offset := time.Now().Truncate(time.Microsecond).In(time.FixedZone("", 3600)).Format(time.RFC3339Nano)
	rebuilt := map[string]struct{ path, body string }{
		"timestamp.offset":    {"/v1/" + g1 + "?readTime=" + url.QueryEscape(offset), ""},
		"timestamp.month_13":  {"", `{"documents":["` + g1 + `"],"readTime":"2026-13-01T00:00:00Z"}`},
		"readtime.year_10000": {"", `{"documents":["` + g1 + `"],"readTime":"10000-01-01T00:00:00Z"}`},
		"oneof.batch_get_new_transaction_and_read_time": {"",
			`{"documents":["` + g1 + `"],"newTransaction":{"readOnly":{}},"readTime":"` + readTime + `"}`},
		"oneof.batch_get_transaction_and_read_time": {"",
			`{"documents":["` + g1 + `"],"transaction":"AAAA","readTime":"` + readTime + `"}`},
		"oneof.run_query_transaction_and_read_time": {"",
			`{"structuredQuery":` + from + `,"transaction":"AAAA","readTime":"` + readTime + `"}`},
		"oneof.run_query_new_transaction_and_read_time": {"",
			`{"structuredQuery":` + from + `,"newTransaction":{"readOnly":{}},"readTime":"` + readTime + `"}`},
	}
	compared, gaps := 0, map[string]bool{}
	for _, c := range f.Cases {
		if c.Transport != "rest" {
			continue
		}
		path := "/v1/" + strings.ReplaceAll(c.Request.Path, "{project}", apitest.Project)
		body := string(c.Request.Body)
		if strings.HasPrefix(body, `"`) { // a raw body, sent as it is
			if err := json.Unmarshal(c.Request.Body, &body); err != nil {
				t.Fatal(err)
			}
		}
		body = strings.ReplaceAll(body, "{project}", apitest.Project)
		if r, ok := rebuilt[c.ID]; ok {
			path, body = cmp.Or(r.path, path), cmp.Or(r.body, body)
		}
		if strings.Contains(path, "<") || strings.Contains(body, "<") {
			t.Errorf("%s: a placeholder is left: %s %s", c.ID, path, body)
			continue
		}
		code, _, got := h.do(t, c.Request.Method, path, body, tok)
		compared++
		ok := code == c.Response.HTTP && sameJSON(t, normalise(t, got), c.Response.Body)
		if reason, known := wireGaps[c.ID]; known {
			gaps[c.ID] = true
			if ok {
				t.Errorf("%s: answered as production, but listed as a gap (%s)", c.ID, reason)
			}
			continue
		}
		if !ok {
			t.Errorf("%s: %s %s %s\n got %d %s\nwant %d %s", c.ID, c.Request.Method, path, body, code, got, c.Response.HTTP, c.Response.Body)
		}
	}
	for _, id := range slices.Sorted(maps.Keys(wireGaps)) {
		if !gaps[id] {
			t.Errorf("%s is listed as a gap but was not sent", id)
		}
	}
	t.Logf("compared %d recorded REST cases, %d of them known gaps", compared, len(gaps))
	if compared < 60 {
		t.Errorf("only %d cases compared", compared)
	}
}

// An id that is not UTF-8 reaches REST only through a percent-encoded path or query parameter.
// It is refused at the edge, so it is never stored and never breaks its collection (0xFF 0x01
// would end its name key's segment early).
func TestIDNotUTF8(t *testing.T) {
	h := newHarness(t, apitest.Options{})
	tok := h.env.JWT(t)
	notUTF8 := func(id string) string {
		return `{"error":{"code":400,"message":"Resource id \"` + id + `\" is invalid because it is not UTF-8.","status":"INVALID_ARGUMENT"}}`
	}
	for _, c := range []struct{ method, path, want string }{
		{http.MethodPatch, "/v1/" + root + "/c/%FF%01", notUTF8(`\uFFFD\u0001`)},
		{http.MethodGet, "/v1/" + root + "/c/%FF%01", notUTF8(`\uFFFD\u0001`)},
		{http.MethodPost, "/v1/" + root + "/u?documentId=%FF%01", notUTF8(`\uFFFD\u0001`)},
		{http.MethodPost, "/v1/" + root + "/%C0", notUTF8(`\uFFFD`)},
		{http.MethodGet, "/v1/projects/%FF/databases/(default)/documents/c/d", notUTF8(`\uFFFD`)},
		{http.MethodGet, "/v1/projects/" + apitest.Project + "/databases/%FF/documents/c/d", notUTF8(`\uFFFD`)},
	} {
		body := ""
		if c.method != http.MethodGet {
			body = `{"fields":{"a":{"integerValue":"1"}}}`
		}
		code, _, got := h.do(t, c.method, c.path, body, tok)
		if code != 400 || !sameJSON(t, got, []byte(c.want)) {
			t.Errorf("%s %s: %d %s\nwant %s", c.method, c.path, code, got, c.want)
		}
	}
	for _, path := range []string{"/v1/" + root + "/c", "/v1/" + root + "/u"} {
		if code, _, got := h.do(t, http.MethodGet, path, "", tok); code != 200 || string(got) != "{}" {
			t.Errorf("list %s: %d %s", path, code, got)
		}
	}
}

// A page token is unsigned: a client can forge one with the right listing digest and any resume
// point. One that is not a document of the listing is refused, never a crash (F1 review M2).
func TestForgedPageToken(t *testing.T) {
	h := newHarness(t, apitest.Options{})
	tok := h.env.JWT(t)
	if code, _, body := h.do(t, http.MethodPatch, "/v1/"+root+"/c/d", `{"fields":{"a":{"integerValue":"2"}}}`, tok); code != 200 {
		t.Fatalf("setup: %d %s", code, body)
	}
	forge := func(collectionID, order string, after []string) string {
		sum := sha256.New()
		for _, s := range []string{root, collectionID, order, "0"} {
			sum.Write([]byte(s))
			sum.Write([]byte{0})
		}
		m := map[string]any{"l": sum.Sum(nil), "a": after}
		if order != "" { // the cursor's value of the ordered field
			v, err := proto.Marshal(&pb.Value{ValueType: &pb.Value_IntegerValue{IntegerValue: 1}})
			if err != nil {
				t.Fatal(err)
			}
			m["v"] = []string{base64.RawURLEncoding.EncodeToString(v)}
		}
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(b)
	}
	const foreign = `{"error":{"code":400,"message":"Invalid page token.","status":"INVALID_ARGUMENT"}}`
	for _, after := range [][]string{{}, {"a"}, {"c"}, {"c", "d", "e"}, {"x", "d"}, {"c", "__x__"}, {"c", "d", "s", "x"}} {
		// ListDocuments of c, and of every collection under the root: ListDocuments with an
		// empty collection id, reached on gRPC alone, shares the check (api.tokenAfter).
		code, _, got := h.do(t, http.MethodGet, "/v1/"+root+"/c?pageToken="+forge("c", "", after), "", tok)
		if code != 400 || string(got) != foreign {
			t.Errorf("after %q: %d %s", after, code, got)
		}
		code, _, got = h.do(t, http.MethodGet, "/v1/"+root+"/c?orderBy=a&pageToken="+forge("c", "a", after), "", tok)
		if code != 400 || string(got) != foreign {
			t.Errorf("ordered, after %q: %d %s", after, code, got)
		}
	}
	// A forged token that names a document of the listing is a cursor like any other.
	if code, _, got := h.do(t, http.MethodGet, "/v1/"+root+"/c?pageToken="+forge("c", "", []string{"c", "a"}), "", tok); code != 200 ||
		!strings.Contains(string(got), root+"/c/d") {
		t.Errorf("a forged cursor: %d %s", code, got)
	}
	if code, _, got := h.do(t, http.MethodGet, "/v1/"+root+"/c?orderBy=a&pageToken="+forge("c", "a", []string{"c", "a"}), "", tok); code != 200 ||
		!strings.Contains(string(got), root+"/c/d") {
		t.Errorf("a forged ordered cursor: %d %s", code, got)
	}
}
