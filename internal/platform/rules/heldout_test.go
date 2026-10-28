package rules_test

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/arumadev/aruma/internal/platform/rules/rulestest"
)

// TestHeldOut measures the engine once against the held-out set (ADR-021 §6), which is not on
// this branch: it runs only when ARUMA_RULES_HELDOUT names a directory where the branch
// oracle-rules-heldout is checked out, outside the repository's work tree, such as
//
//	git worktree add /path/to/heldout origin/oracle-rules-heldout
//	ARUMA_RULES_HELDOUT=/path/to/heldout/test/oracle/rules \
//	  go test ./internal/platform/rules -run TestHeldOut -v
//
// It prints the agreement by provenance and source and the categories that disagree, and fails
// below the threshold or on any permissive allow, as TestOracle does. The numbers go into
// docs/reference/compatibility.md as they come out; the engine is not tuned on them.
func TestHeldOut(t *testing.T) {
	root := os.Getenv("ARUMA_RULES_HELDOUT")
	if root == "" {
		t.Skip("ARUMA_RULES_HELDOUT is not set: the held-out set is measured once, by hand")
	}
	set, err := rulestest.LoadHeldOut(root)
	if err != nil {
		t.Fatal(err)
	}
	runner := rulestest.NewRunner()
	var rep rulestest.Report
	for _, c := range set.Cases {
		rep.Add(runner.Run(context.Background(), c))
	}
	t.Logf("agreement with the held-out set, %d cases from %d files:\n%s", len(set.Cases), len(set.Files), rep.Table())
	t.Logf("by category, where a counted case disagrees:\n%s", rep.CategoryTable(false))
	t.Logf("excluded, by reason:\n%s", rep.ExcludedList())
	for _, f := range rulestest.Faults(&rep) {
		t.Error(f)
	}
	for _, f := range rulestest.Accept(&rep, nil, oracleThreshold) {
		t.Error(f)
	}
	// The threshold holds for each source of a provenance too.
	type key struct {
		prov   rulestest.Provenance
		source string
	}
	counted, agree := map[key]int{}, map[key]int{}
	for _, res := range rep.Results {
		if res.Excluded != "" {
			continue
		}
		k := key{res.Case.Provenance, res.Case.Source}
		counted[k]++
		if res.Agree {
			agree[k]++
		}
	}
	var keys []key
	for k := range counted {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return fmt.Sprint(keys[i]) < fmt.Sprint(keys[j]) })
	var lines []string
	for _, k := range keys {
		ratio := 100 * float64(agree[k]) / float64(counted[k])
		lines = append(lines, fmt.Sprintf("%s/%s: %d of %d (%.1f%%)", k.prov, k.source, agree[k], counted[k], ratio))
		if ratio < oracleThreshold {
			t.Errorf("%s/%s: %d of %d counted cases agree (%.1f%%), below %.0f%%", k.prov, k.source, agree[k], counted[k], ratio, oracleThreshold)
		}
	}
	t.Logf("by provenance and source: %s", strings.Join(lines, "; "))
	for _, res := range rep.Disagreements(0) {
		t.Log(res.Line())
	}
}
