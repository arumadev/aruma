package documents

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	conformance "github.com/arumadev/aruma/test/conformance/documents"
)

// shrinkBudget bounds the requests one shrink sends to the server.
const shrinkBudget = 200

// TestReplayCorpusAgainstAruma replays every replayable corpus request against Aruma, after the
// file's setup, and diffs the normalised answer with production's. It runs with
// ARUMA_DOCUMENTS_URL and fails when that server cannot be reached. A difference names its JSON
// paths; when the request has a body, the smallest body that still differs is reported too.
func TestReplayCorpusAgainstAruma(t *testing.T) {
	files, err := LoadReplay(conformance.CorpusDir)
	if err != nil {
		t.Fatal(err)
	}
	replayAll(t, Replayer{Target: conformance.Aruma(t)}, files)
}

func replayAll(t *testing.T, r Replayer, files []ReplayFile) {
	t.Helper()
	for _, f := range files {
		t.Run(f.Name, func(t *testing.T) {
			if err := r.Setup(t.Context(), f); err != nil {
				t.Fatal(err)
			}
			for _, c := range f.Cases {
				t.Run(c.ID, func(t *testing.T) {
					diff := func(body any) []string { return r.Compare(t.Context(), f, c, body) }
					d := diff(c.Body)
					if len(d) == 0 {
						return
					}
					t.Errorf("%s %s differs from production:\n  %s", c.Method, c.Path, strings.Join(d, "\n  "))
					if c.Body != nil {
						smallest := Shrink(c.Body, func(b any) bool { return len(diff(b)) > 0 }, shrinkBudget)
						t.Logf("smallest body that still differs: %s", compact(smallest))
					}
				})
			}
		})
	}
}

// fakeServer answers like production from the corpus, as a live server would: the live project
// in names, real times where the recording has placeholders. mutate may change an answer.
func fakeServer(t *testing.T, files []ReplayFile, project string, mutate func(id string, body any) any) *httptest.Server {
	t.Helper()
	type answer struct {
		id   string
		http int
		body any
	}
	answers := map[string]answer{}
	for _, f := range files {
		for _, c := range f.Cases {
			answers[c.Method+" "+c.Path+" "+compact(c.Body)] = answer{f.Name + "#" + c.ID, c.HTTP, c.Response}
		}
	}
	live := func(v any) any {
		b, _ := json.Marshal(v)
		s := strings.ReplaceAll(string(b), "projects/chatapp-8ae21/", "projects/"+project+"/")
		s = regexp.MustCompile(`"<\w+: RFC 3339>"`).ReplaceAllString(s, `"2026-10-26T18:00:00.123456Z"`)
		var o any
		_ = json.Unmarshal([]byte(s), &o)
		return o
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body any
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			_ = json.Unmarshal(b, &body)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "PATCH" {
			fmt.Fprint(w, "{}")
			return
		}
		path := strings.Replace(strings.TrimPrefix(r.URL.RequestURI(), "/v1/"), "projects/"+project+"/", "projects/{project}/", 1)
		b, _ := json.Marshal(body)
		key := r.Method + " " + path + " " + strings.ReplaceAll(string(b), "projects/"+project+"/", "projects/{project}/")
		a, ok := answers[key]
		if !ok {
			w.WriteHeader(http.StatusTeapot)
			fmt.Fprintf(w, `{"error":{"code":418,"message":%q,"status":"UNKNOWN"}}`, "not recorded: "+key)
			return
		}
		w.WriteHeader(a.http)
		_ = json.NewEncoder(w).Encode(mutate(a.id, live(a.body)))
	}))
}

// TestReplayAgainstFakeServer drives the whole replay (setup, raw requests, normalising, diff)
// against a server that answers like production: no case differs. Then one changed message is
// found, at its path.
func TestReplayAgainstFakeServer(t *testing.T) {
	files, err := LoadReplay(conformance.CorpusDir)
	if err != nil {
		t.Fatal(err)
	}
	const project = "demo-aruma"
	same := fakeServer(t, files, project, func(_ string, b any) any { return b })
	defer same.Close()
	r := Replayer{Target: &conformance.HTTPTarget{BaseURL: same.URL, ProjectID: project, Client: same.Client()}}
	replayAll(t, r, files)

	const victim = "f1-get.json#rest.missing"
	changed := fakeServer(t, files, project, func(id string, b any) any {
		if id == victim {
			return map[string]any{"error": map[string]any{"code": 404, "message": "Document not found.", "status": "NOT_FOUND"}}
		}
		return b
	})
	defer changed.Close()
	r.Target = &conformance.HTTPTarget{BaseURL: changed.URL, ProjectID: project, Client: changed.Client()}
	for _, f := range files {
		for _, c := range f.Cases {
			d := r.Compare(t.Context(), f, c, c.Body)
			if want := f.Name+"#"+c.ID == victim; (len(d) > 0) != want || (want && !strings.HasPrefix(d[0], "$.error.message")) {
				t.Errorf("%s#%s: diff %v, want a difference %v", f.Name, c.ID, d, want)
			}
		}
	}
}

// compact is v as JSON, with <, > and & as they are: json.Marshal escapes them (\u003c), which
// would hide a placeholder from a search for "<.
func compact(v any) string {
	var b strings.Builder
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	_ = e.Encode(v)
	return strings.TrimSuffix(b.String(), "\n")
}

// TestReplayCorpusOffline: the replayable cases load, each file has a setup, no replayed request
// holds a placeholder (it would not be the request production answered), and a recording
// normalised and diffed with itself shows no difference.
func TestReplayCorpusOffline(t *testing.T) {
	files, err := LoadReplay(conformance.CorpusDir)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, f := range files {
		if len(f.Setup) == 0 {
			t.Errorf("%s: no setup", f.Name)
		}
		for _, c := range f.Cases {
			n++
			if strings.Contains(c.Path, "<") || strings.Contains(compact(c.Body), "\"<") {
				t.Errorf("%s: %s: the request holds a placeholder", f.Name, c.ID)
			}
			if !strings.HasPrefix(c.Path, "projects/{project}/") {
				t.Errorf("%s: %s: path %q does not start with projects/{project}/", f.Name, c.ID, c.Path)
			}
			if d := Diff(c.Response, Normalise(c.Response, f.Project, f.Project)); len(d) != 0 {
				t.Errorf("%s: %s: normalising a recording changes it: %v", f.Name, c.ID, d)
			}
		}
	}
	if n < 50 {
		t.Errorf("%d replayable cases; the F1 recordings mark more than 50", n)
	}
}

// TestNormaliseAndDiff: a live answer with other times, transaction id and project normalises to
// the recording; a changed message, a missing or extra key and a reordered list are each
// reported at their path.
func TestNormaliseAndDiff(t *testing.T) {
	recorded := map[string]any{
		"name":       "projects/chatapp-8ae21/databases/(default)/documents/c/d",
		"createTime": "<createTime: RFC 3339>",
		"updateTime": "<updateTime: RFC 3339>",
		"fields":     map[string]any{"a": map[string]any{"integerValue": "1"}},
	}
	live := map[string]any{
		"name":       "projects/demo/databases/(default)/documents/c/d",
		"createTime": "2026-10-26T18:00:00.123456Z",
		"updateTime": "2026-10-26T18:00:01.654321Z",
		"fields":     map[string]any{"a": map[string]any{"integerValue": "1"}},
	}
	if d := Diff(recorded, Normalise(live, "demo", "chatapp-8ae21")); len(d) != 0 {
		t.Errorf("normalised live answer differs: %v", d)
	}
	msg := func(s string) any { return map[string]any{"error": map[string]any{"message": s}} }
	if d := Diff(msg("The database x does not exist for project chatapp-8ae21 Please"), Normalise(msg("The database x does not exist for project demo Please"), "demo", "chatapp-8ae21")); len(d) != 0 {
		t.Errorf("project in a message: %v", d)
	}
	if got := Normalise([]any{map[string]any{"transaction": "QUJD"}}, "demo", "p"); !reflect.DeepEqual(got, []any{map[string]any{"transaction": "<transaction>"}}) {
		t.Errorf("transaction: %v", got)
	}
	for _, tc := range []struct {
		name      string
		want, got any
		path      string
	}{
		{"message", msg("a"), msg("b"), "$.error.message"},
		{"missing key", map[string]any{"a": 1.0, "b": 2.0}, map[string]any{"a": 1.0}, "$.b: missing"},
		{"extra key", map[string]any{"a": 1.0}, map[string]any{"a": 1.0, "details": []any{}}, "$.details: unexpected"},
		{"order", []any{"x", "y"}, []any{"y", "x"}, "$[0]"},
		{"length", []any{"x"}, []any{"x", "y"}, "$: 2 elements, want 1"},
	} {
		d := Diff(tc.want, tc.got)
		if !slices.ContainsFunc(d, func(s string) bool { return strings.HasPrefix(s, tc.path) }) {
			t.Errorf("%s: %v does not report %s", tc.name, d, tc.path)
		}
	}
}

// TestShrink: a body that differs because of one document among many shrinks to that document
// alone, and the budget bounds the calls.
func TestShrink(t *testing.T) {
	body := map[string]any{
		"documents": []any{"projects/p/databases/(default)/documents/c/a", "projects/p/databases/(default)/documents/c/bad", "projects/p/databases/(default)/documents/c/b", "projects/p/databases/(default)/documents/c/c"},
		"mask":      map[string]any{"fieldPaths": []any{"x", "y"}},
	}
	fails := func(v any) bool {
		return strings.Contains(compact(v), "/bad")
	}
	got := Shrink(body, fails, 1000)
	want := map[string]any{"documents": []any{"projects/p/databases/(default)/documents/c/bad"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("shrunk to %s, want %s", compact(got), compact(want))
	}
	calls := 0
	Shrink(body, func(any) bool { calls++; return false }, 5)
	if calls != 5 {
		t.Errorf("%d calls with a budget of 5", calls)
	}
}
