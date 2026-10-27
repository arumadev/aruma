package documents

import (
	"io"
	"log/slog"
	"maps"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/arumadev/aruma/internal/platform/httpx"
	"github.com/arumadev/aruma/internal/services/documents/api"
	"github.com/arumadev/aruma/internal/services/documents/api/apitest"
	"github.com/arumadev/aruma/internal/services/documents/query"
	"github.com/arumadev/aruma/internal/services/documents/rest"
	conformance "github.com/arumadev/aruma/test/conformance/documents"
)

// differs are the replayable cases whose answer Aruma does not reproduce, each with the id of its
// parity exception (parity-exceptions.json). A listed case that matches fails the test, so the
// list stays true.
var differs = map[string]string{
	"f3-query-limits.json ineq.11_fields": "inequality-fields-order",
}

// TestReplayCorpusInProcess replays every replayable corpus case against an Aruma in this
// process, on a fresh database, with no environment variable: the REST surface under Common's
// body cap, the service layer and core on Postgres. The project is the recordings' own, so the
// offsets production's messages count and the console links' payloads replay byte for byte; only
// what Normalise replaces (times, transaction ids, page tokens) may differ.
//
// The F3 cases are replayed twice, by the planner, which serves some of them from an index, and
// by the reference path alone: both must answer as production, so they agree with each other
// too (an aggregation's sum among them, whatever order each reads the documents in).
func TestReplayCorpusInProcess(t *testing.T) {
	files, err := LoadReplay(conformance.CorpusDir)
	if err != nil {
		t.Fatal(err)
	}
	env := apitest.New(t, apitest.Options{})
	token := env.JWT(t)
	target := func(svc *api.Service) Replayer {
		log := slog.New(slog.NewTextHandler(io.Discard, nil))
		ts := httptest.NewServer(httpx.CommonWithLimit(rest.New(rest.Options{Service: svc, Logger: log}), log, nil, 17<<20))
		t.Cleanup(ts.Close)
		return Replayer{Target: &conformance.HTTPTarget{BaseURL: ts.URL, ProjectID: apitest.Project, Token: token, Client: ts.Client()}}
	}
	planner, reference := target(env.Service), target(env.Reference)

	compared, f3 := 0, 0
	seen := map[string]bool{}
	indexed := servedByIndex()
	for _, f := range files {
		if err := planner.Setup(t.Context(), f); err != nil {
			t.Fatal(err)
		}
		paths := []Replayer{planner}
		if strings.HasPrefix(f.Name, "f3-") {
			paths = append(paths, reference)
		}
		for _, c := range f.Cases {
			compared++
			if strings.HasPrefix(f.Name, "f3-") {
				f3++
			}
			id := f.Name + " " + c.ID
			for i, r := range paths {
				d := r.Compare(t.Context(), f, c, c.Body)
				name := map[bool]string{true: "planner", false: "reference path"}[i == 0]
				if exception, ok := differs[id]; ok {
					seen[id] = true
					if len(d) == 0 {
						t.Errorf("%s (%s): matches production, but is listed as differing (%s)", id, name, exception)
					}
					continue
				}
				if len(d) > 0 {
					t.Errorf("%s (%s): %s %s differs from production:\n  %s", id, name, c.Method, c.Path, strings.Join(d, "\n  "))
				}
			}
		}
	}
	for _, id := range slices.Sorted(maps.Keys(differs)) {
		if !seen[id] {
			t.Errorf("%s is listed as differing but is not a replayable case", id)
		}
	}
	t.Logf("replayed %d cases (%d of F3, by the planner and the reference path), %d listed as differing", compared, f3, len(differs))
	if f3 != 343 {
		t.Errorf("%d F3 cases replayed, want the 343 marked replay", f3)
	}
	if servedByIndex() == indexed {
		t.Error("the planner served no replayed case from an index")
	}
}

func servedByIndex() int64 {
	return query.ServedCount(query.PathIndex) + query.ServedCount(query.PathIndexMerged) + query.ServedCount(query.PathIndexContains)
}
