package documents

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"testing"
)

// allCases is every case of every phase written so far.
func allCases() []Case {
	return slices.Concat(f1Cases)
}

type phaseList struct {
	Phases []string `json:"phases"`
	Closed []string `json:"closed"`
}

// TestMustCatalogue: the 27 MUSTs are numbered 1 to 27 with a known phase, every case names
// valid MUSTs and a known phase and has a unique id, and every MUST of a phase that
// test/oracle/documents/phases.json marks closed is covered by at least one case.
func TestMustCatalogue(t *testing.T) {
	var ph phaseList
	b, err := os.ReadFile("../../oracle/documents/phases.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &ph); err != nil {
		t.Fatal(err)
	}
	if len(Musts) != 27 {
		t.Errorf("%d MUSTs, the research lists 27", len(Musts))
	}
	for i, m := range Musts {
		if m.N != i+1 || m.Text == "" || !slices.Contains(ph.Phases, m.Phase) {
			t.Errorf("MUST %d (position %d): number, text or phase %q wrong", m.N, i+1, m.Phase)
		}
	}
	covered := map[int]bool{}
	seen := map[string]bool{}
	for _, c := range allCases() {
		if c.ID == "" || seen[c.ID] {
			t.Errorf("case id %q empty or repeated", c.ID)
		}
		seen[c.ID] = true
		if !slices.Contains(ph.Phases, c.Phase) {
			t.Errorf("%s: phase %q", c.ID, c.Phase)
		}
		if len(c.Steps) == 0 {
			t.Errorf("%s: no steps", c.ID)
		}
		for _, n := range c.Must {
			if n < 1 || n > len(Musts) {
				t.Errorf("%s: MUST %d", c.ID, n)
			}
			covered[n] = true
		}
	}
	for _, m := range Musts {
		if slices.Contains(ph.Closed, m.Phase) && !covered[m.N] {
			t.Errorf("MUST %d is owed by %s, which phases.json marks closed, and no case covers it: %s", m.N, m.Phase, m.Text)
		}
	}
}

// TestRecordedTargetPinsRequests: a recorded answer is given only to the request that was
// recorded; a placeholder in the recording stands for any value; a setup step gets an empty 200.
func TestRecordedTargetPinsRequests(t *testing.T) {
	rec, err := NewRecorded(CorpusDir)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Project() == "" || rec.Project() == "{project}" {
		t.Fatalf("project %q", rec.Project())
	}
	root := "projects/" + rec.Project() + "/databases/(default)/documents"
	do := func(ref string, req Request) error {
		_, err := rec.Do(t.Context(), Step{Recorded: ref}, req)
		return err
	}
	refused := map[string]error{
		"other path":     do("f1-get.json#rest.missing", Request{Method: "GET", Path: root + "/aruma-ref-f1-get/g1"}),
		"other method":   do("f1-get.json#rest.missing", Request{Method: "DELETE", Path: root + "/aruma-ref-f1-get/nope"}),
		"other body":     do("f1-batch-get.json#rest.duplicates", Request{Method: "POST", Path: root + ":batchGet", Body: map[string]any{"documents": []any{root + "/aruma-ref-f1-get/g1"}}}),
		"unknown case":   do("f1-get.json#rest.no-such-case", Request{Method: "GET", Path: root}),
		"grpc recording": do("f1-get.json#grpc.found", Request{Method: "GET", Path: root}),
		"no recording":   do("", Request{Method: "GET", Path: root}),
	}
	for name, err := range refused {
		if err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if err := do("f1-list-documents.json#rest.paging.page2", Request{Method: "GET", Path: root + "/aruma-ref-f1-list?pageSize=2&pageToken=anything"}); err != nil {
		t.Errorf("a placeholder did not match: %v", err)
	}
	if err := do("f1-list-documents.json#rest.paging.page2", Request{Method: "GET", Path: root + "/aruma-ref-f1-list?pageSize=2&pageToken=x&pageSize=3"}); err == nil {
		t.Error("a placeholder matched past the next &")
	}
	r, err := rec.Do(t.Context(), Step{Setup: true}, Request{Method: "PATCH", Path: root + "/x/y"})
	if err != nil || r.HTTP != 200 {
		t.Errorf("setup step: %d %v", r.HTTP, err)
	}
}

type fakeT struct {
	skipped, fatal bool
}

func (f *fakeT) Helper()                   {}
func (f *fakeT) Skipf(string, ...any)      { f.skipped = true }
func (f *fakeT) Fatalf(string, ...any)     { f.fatal = true }
func (f *fakeT) result() (skip, fail bool) { return f.skipped, f.fatal }

// TestArumaTargetNeverSkipsWhenAsked: without ARUMA_DOCUMENTS_URL the Aruma half skips; with it,
// a server that is down, or that does not speak the Firestore REST protocol, fails; a server that
// answers the protocol is used.
func TestArumaTargetNeverSkipsWhenAsked(t *testing.T) {
	t.Setenv(EnvURL, "")
	f := &fakeT{}
	aruma(f, func() (*HTTPTarget, error) { return &HTTPTarget{}, nil })
	if skip, fail := f.result(); !skip || fail {
		t.Errorf("unset: skip %v, fail %v; want a skip", skip, fail)
	}

	protocol := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":{"code":404,"message":"not found","status":"NOT_FOUND"}}`)
	}))
	defer protocol.Close()
	html := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, "<html>not here</html>")
	}))
	defer html.Close()
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()

	for _, tc := range []struct {
		name, url, project string
		ok                 bool
	}{
		{"answers the protocol", protocol.URL, "demo-aruma", true},
		{"not the protocol", html.URL, "demo-aruma", false},
		{"down", down.URL, "demo-aruma", false},
		{"no project", protocol.URL, "", false},
	} {
		t.Setenv(EnvURL, tc.url)
		t.Setenv(EnvProject, tc.project)
		f := &fakeT{}
		got := aruma(f, func() (*HTTPTarget, error) {
			h, _, err := ArumaFromEnv(t.Context())
			return h, err
		})
		skip, fail := f.result()
		if skip || fail == tc.ok || (got != nil) != tc.ok {
			t.Errorf("%s: skip %v, fail %v, target %v; want ok %v", tc.name, skip, fail, got != nil, tc.ok)
		}
	}
}

// TestChecksRefuse: the checks refuse what production does not send.
func TestChecksRefuse(t *testing.T) {
	ctx := &Context{Target: &HTTPTarget{ProjectID: "p"}, vars: map[string]string{}, req: Request{Method: "GET", Path: "projects/p/databases/(default)/documents/c/d"}}
	errBody := func(code int, status, msg string) any {
		return map[string]any{"error": map[string]any{"code": float64(code), "status": status, "message": msg}}
	}
	refused := map[string]error{
		"wrong message":     Error(404, "NOT_FOUND", "x")(ctx, Response{HTTP: 404, Body: errBody(404, "NOT_FOUND", "y")}),
		"code not HTTP":     Error(404, "NOT_FOUND", "x")(ctx, Response{HTTP: 404, Body: errBody(400, "NOT_FOUND", "x")}),
		"extra key":         Error(404, "NOT_FOUND", "x")(ctx, Response{HTTP: 404, Body: map[string]any{"error": errBody(404, "NOT_FOUND", "x").(map[string]any)["error"], "x": 1}}),
		"array for unary":   Error(404, "NOT_FOUND", "x")(ctx, Response{HTTP: 404, Body: []any{errBody(404, "NOT_FOUND", "x")}}),
		"nanoseconds":       Timestamps("t")(ctx, Response{Body: map[string]any{"t": "2026-01-01T00:00:00.123456789Z"}}),
		"offset":            Timestamps("t")(ctx, Response{Body: map[string]any{"t": "2026-01-01T00:00:00.123456+01:00"}}),
		"placeholder live":  Timestamps("t")(ctx, Response{Body: map[string]any{"t": "<t: RFC 3339>"}}),
		"short auto id":     AutoID("name", "c")(ctx, Response{Body: map[string]any{"name": "projects/p/databases/(default)/documents/c/abc"}}),
		"auto id alphabet":  AutoID("name", "c")(ctx, Response{Body: map[string]any{"name": "projects/p/databases/(default)/documents/c/aaaaaaaaaaaaaaaaaaa-"}}),
		"answered twice":    BatchGetAnswers(nil, []string{"c/x"})(ctx, Response{Body: []any{map[string]any{"missing": "projects/p/databases/(default)/documents/c/x", "readTime": "t"}, map[string]any{"missing": "projects/p/databases/(default)/documents/c/x", "readTime": "t"}}}),
		"no readTime":       BatchGetAnswers(nil, []string{"c/x"})(ctx, Response{Body: []any{map[string]any{"missing": "projects/p/databases/(default)/documents/c/x"}}}),
		"transaction later": TransactionFirst()(ctx, Response{Body: []any{map[string]any{"missing": "m", "readTime": "t"}, map[string]any{"transaction": "x"}}}),
	}
	for name, err := range refused {
		if err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if err := Timestamps("t")(ctx, Response{Body: map[string]any{"t": "2026-01-01T00:00:00.123456Z"}}); err != nil {
		t.Errorf("a microsecond time refused: %v", err)
	}
	ctx.req.Path = "projects/p/databases/(default)/documents:batchGet"
	if err := Error(400, "INVALID_ARGUMENT", "x")(ctx, Response{HTTP: 400, Body: []any{errBody(400, "INVALID_ARGUMENT", "x")}}); err != nil {
		t.Errorf("a streaming method's array-wrapped error refused: %v", err)
	}
}
