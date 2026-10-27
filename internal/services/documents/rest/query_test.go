package rest_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/arumadev/aruma/internal/services/documents/api/apitest"
)

// TestIndexesReplay sends the 105 recorded shapes of indexes-105.json through :runQuery and
// :runAggregationQuery, on the never-written collection and on the populated one seeded as the
// recorder seeded it: the status, the needed index's message byte for byte, and the documents
// in order or the aggregates must be production's.
func TestIndexesReplay(t *testing.T) {
	h := newHarness(t, apitest.Options{})
	tok := h.env.JWT(t)
	for _, d := range apitest.IndexSeed() {
		if code, _, b := h.do(t, http.MethodPatch, "/v1/"+root+"/"+d.Path, `{"fields":`+d.Fields+`}`, tok); code != 200 {
			t.Fatalf("seed %s: %d %s", d.Path, code, b)
		}
	}
	matched := 0
	for _, c := range apitest.IndexCases(t) {
		path := "/v1/" + strings.ReplaceAll(c.Request.Path, "{project}", apitest.Project)
		for _, empty := range []bool{true, false} {
			code, _, b := h.do(t, c.Request.Method, path, c.Body(root, empty), tok)
			if diff := indexOutcomeDiff(code, b, c.Want(empty)); diff != "" {
				t.Errorf("%s (empty %v, %s): %s\n got %d %s", c.ID, empty, c.Description, diff, code, b)
				continue
			}
			matched++
		}
	}
	if matched != 210 {
		t.Errorf("%d of 210 answers as production's", matched)
	}
}

// indexOutcomeDiff is how an answer differs from the recorded outcome, or "".
func indexOutcomeDiff(code int, b []byte, want apitest.IndexOutcome) string {
	var elems []struct {
		Error *struct {
			Status  string `json:"status"`
			Message string `json:"message"`
		} `json:"error"`
		Document *struct {
			Name string `json:"name"`
		} `json:"document"`
		Result *struct {
			AggregateFields json.RawMessage `json:"aggregateFields"`
		} `json:"result"`
	}
	if err := json.Unmarshal(b, &elems); err != nil {
		return "not an array: " + err.Error()
	}
	if code != want.HTTP {
		return "status differs"
	}
	if want.Verdict != "ok" {
		if len(elems) != 1 || elems[0].Error == nil || elems[0].Error.Status != want.Verdict || elems[0].Error.Message != want.Message {
			return "want " + want.Verdict + " " + want.Message
		}
		return ""
	}
	var ids []string
	var aggs []json.RawMessage
	for _, e := range elems {
		switch {
		case e.Error != nil:
			return "an error element"
		case e.Document != nil:
			ids = append(ids, apitest.LastSegments(e.Document.Name))
		case e.Result != nil:
			aggs = append(aggs, e.Result.AggregateFields)
		}
	}
	if want.Aggregates != nil {
		if len(aggs) != len(want.Aggregates) {
			return "aggregates differ"
		}
		for i := range aggs {
			var x, y any
			if json.Unmarshal(aggs[i], &x) != nil || json.Unmarshal(want.Aggregates[i], &y) != nil || !jsonEqual(x, y) {
				return "aggregates differ"
			}
		}
		return ""
	}
	if !slices.Equal(ids, want.IDs) && (len(ids) != 0 || len(want.IDs) != 0) {
		return "ids differ, want " + strings.Join(want.IDs, " ")
	}
	return ""
}

func jsonEqual(x, y any) bool {
	a, _ := json.Marshal(x)
	b, _ := json.Marshal(y)
	return string(a) == string(b)
}
