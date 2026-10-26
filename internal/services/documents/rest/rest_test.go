package rest_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/arumadev/aruma/internal/platform/httpx"
	"github.com/arumadev/aruma/internal/services/documents/api/apitest"
	"github.com/arumadev/aruma/internal/services/documents/rest"
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
var unserved = []string{":commit", ":beginTransaction", ":rollback", ":runQuery", ":runAggregationQuery", ":batchWrite", ":partitionQuery"}

// TestCorpusReplay replays the F1 recordings' REST cases in their order on one database: the
// setup commits as updates, then every case whose request carries no placeholder and needs no
// later phase (transactions, Commit). Its status and body must be production's, the times and
// page tokens normalised as the recorder normalised them.
func TestCorpusReplay(t *testing.T) {
	h := newHarness(t, apitest.Options{})
	tok := h.env.JWT(t)
	dir := filepath.Join("..", "..", "..", "..", "test", "oracle", "documents", "corpus")
	compared, skipped := 0, 0
	reproduced := map[string]bool{}
	for _, name := range []string{"f1-get.json", "f1-batch-get.json", "f1-create.json", "f1-update.json", "f1-delete.json",
		"f1-list-documents.json", "f1-list-collection-ids.json", "f1-resource-names.json"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		var f corpusFile
		if err := json.Unmarshal(b, &f); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, c := range f.Setup.Commits {
			for _, w := range c.Writes {
				if w.Update == nil {
					t.Fatalf("%s: a setup write that is not an update", name)
				}
				path := "/v1/" + strings.ReplaceAll(w.Update.Name, "{project}", apitest.Project)
				if code, _, body := h.do(t, http.MethodPatch, path, `{"fields":`+string(w.Update.Fields)+`}`, tok); code != 200 {
					t.Fatalf("%s setup %s: %d %s", name, path, code, body)
				}
			}
		}
		for _, c := range f.Cases {
			path, body := c.Request.Path, string(c.Request.Body)
			if c.Transport != "rest" || strings.Contains(path, "<") || strings.Contains(body, "<") ||
				slices.ContainsFunc(unserved, func(v string) bool { return strings.Contains(path, v) }) ||
				strings.Contains(path, "transaction=") || strings.Contains(body, `"newTransaction"`) || strings.Contains(body, `"transaction"`) {
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
			want := string(c.Response.Body)
			if strings.Contains(want, "<auto id") {
				skipped++
				continue
			}
			compared++
			ok := code == c.Response.HTTP && sameJSON(t, normalise(t, got), []byte(want))
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

// normalise replaces what the recorder replaced (lib.mjs): times and page tokens.
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
				case "createTime", "updateTime", "readTime":
					if _, ok := e.(string); ok {
						x[k] = "<" + k + ": RFC 3339>"
						continue
					}
				case "nextPageToken":
					x[k] = "<nextPageToken>"
					continue
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
	code, _, _ = h.do(t, http.MethodPost, "/v1/projects/"+apitest.Project+"/databases/(default)/documents:commit", `{}`, tok)
	if code != http.StatusNotImplemented {
		t.Errorf(":commit: %d", code)
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
