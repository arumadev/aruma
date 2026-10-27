package rest_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arumadev/aruma/internal/services/documents/api/apitest"
)

// queryUnimplemented are the recorded query cases this server answers UNIMPLEMENTED by design,
// after the index check production makes first: Query Explain's plans and statistics.
var queryUnimplemented = map[string]bool{
	"f0-explain.json rest.query.analyze_false":       true,
	"f0-explain.json rest.query.analyze_true":        true,
	"f0-explain.json rest.query.options_empty":       true,
	"f0-explain.json rest.aggregation.analyze_false": true,
	"f0-explain.json rest.aggregation.analyze_true":  true,
}

// TestQueryRecordingsReplay replays the F0 recordings' REST query cases (runQuery,
// runAggregationQuery, partitionQuery) on their recorded data: kindless and collection-group
// descendants as the CLI and the Node SDK's recursiveDelete ask them, partitions, find_nearest
// without a vector index, Query Explain, the query limits, and a missing database. f0-errors.json
// keeps no data, so its answers that are not errors are compared by status alone.
func TestQueryRecordingsReplay(t *testing.T) {
	h := newHarness(t, apitest.Options{})
	tok := h.env.JWT(t)
	seed := func(path, fields string) {
		t.Helper()
		if code, _, b := h.do(t, http.MethodPatch, "/v1/"+root+"/"+path, `{"fields":`+fields+`}`, tok); code != 200 {
			t.Fatalf("seed %s: %d %s", path, code, b)
		}
	}
	for path, v := range map[string]int{"aruma-ref-f0-tree/a": 1, "aruma-ref-f0-tree/a/other/z": 4, "aruma-ref-f0-tree/a/sub/x": 2,
		"aruma-ref-f0-tree/a/sub/x/deep/y": 3, "aruma-ref-f0-tree/b": 5, "aruma-ref-f0-tree/m/kids/k1": 6, "aruma-ref-f0-tree2/q": 7} {
		seed(path, fmt.Sprintf(`{"v":{"integerValue":"%d"}}`, v))
	}
	vector := `{"mapValue":{"fields":{"__type__":{"stringValue":"__vector__"},"value":{"arrayValue":{"values":[%s]}}}}}`
	seed("aruma-ref-f0-vec/v1", `{"k":{"stringValue":"a"},"emb":`+fmt.Sprintf(vector, `{"doubleValue":1},{"doubleValue":2},{"doubleValue":3}`)+`}`)
	seed("aruma-ref-f0-vec/v2", `{"k":{"stringValue":"b"},"emb":`+fmt.Sprintf(vector, `{"doubleValue":2},{"doubleValue":3},{"doubleValue":4}`)+`}`)
	for n := 1; n <= 3; n++ {
		seed(fmt.Sprintf("aruma-ref-f0-explain/e%d", n), fmt.Sprintf(`{"n":{"integerValue":"%d"}}`, n))
	}

	dir := filepath.Join("..", "..", "..", "..", "test", "oracle", "documents", "corpus")
	compared := 0
	for _, name := range []string{"f0-descendants.json", "f0-find-nearest.json", "f0-explain.json", "f0-errors.json", "f0-missing-database.json"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		var f corpusFile
		if err := json.Unmarshal(b, &f); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, c := range f.Cases {
			path, body := c.Request.Path, string(c.Request.Body)
			if c.Transport != "rest" || !strings.Contains(path, "Query") || strings.Contains(body, "<") {
				continue
			}
			path = "/v1/" + strings.ReplaceAll(path, "{project}", apitest.Project)
			code, _, got := h.do(t, c.Request.Method, path, strings.ReplaceAll(body, "{project}", apitest.Project), tok)
			compared++
			if queryUnimplemented[name+" "+c.ID] {
				if code != http.StatusNotImplemented {
					t.Errorf("%s %s: %d %s, want UNIMPLEMENTED", name, c.ID, code, got)
				}
				continue
			}
			want := c.Response.Body
			ok := code == c.Response.HTTP
			if name != "f0-errors.json" || code != 200 {
				ok = ok && sameJSON(t, normalise(t, got), normalise(t, want))
			}
			if !ok {
				t.Errorf("%s %s: %s %s\n got %d %s\nwant %d %s", name, c.ID, path, body, code, got, c.Response.HTTP, want)
			}
		}
	}
	if compared < 30 {
		t.Errorf("only %d recorded query cases compared", compared)
	}
	t.Logf("compared %d recorded query cases", compared)
}
