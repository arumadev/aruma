package rules_test

import (
	"testing"

	"github.com/arumadev/aruma/internal/platform/rules"
)

// The budgets on what a ruleset holds (regex_bytes, and a quarter of compiled_rulesets_bytes for
// its weight) leave room over every file of the oracle that compiles: the largest weighs a
// sixth of the share, and the most its literal patterns take together is a hundredth of
// regex_bytes (the review's SR3-02, 2026-10-24: 5,547,839 bytes for size_functions_950, and
// 38,785 for strings/size_ascii, its classes counted).
func TestCorpusFitsTheBudgets(t *testing.T) {
	set := loadOracle(t)
	lim := rules.DefaultLimits()
	var maxW, maxRx int64
	var wID, rxID string
	n := 0
	for _, cc := range compileCases(set) {
		rs, _ := rules.Compile(cc.svc, rules.File{Name: cc.name, Content: cc.src}, lim)
		if rs == nil {
			continue
		}
		n++
		if w := rs.Weight(); w > maxW {
			maxW, wID = w, cc.ids[0]
		}
		if w := rules.LiteralRegexBytes(rs); w > maxRx {
			maxRx, rxID = w, cc.ids[0]
		}
	}
	t.Logf("%d files compile: the heaviest weighs %d (%s), the most literal patterns take %d (%s)", n, maxW, wID, maxRx, rxID)
	if share := lim.CompiledRulesetsBytes / 4; maxW > share/4 {
		t.Errorf("%s weighs %d, more than a quarter of the %d a ruleset may", wID, maxW, share)
	}
	if maxRx > lim.RegexBytes/16 {
		t.Errorf("%s's patterns take %d, more than a sixteenth of regex_bytes %d", rxID, maxRx, lim.RegexBytes)
	}
}
