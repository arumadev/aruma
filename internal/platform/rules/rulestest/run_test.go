package rulestest

import (
	"context"
	"strings"
	"testing"

	"github.com/arumadev/aruma/internal/platform/rules"
)

func evaluated(outcomes ...rules.Outcome) Got {
	g := Got{Compiled: true, Ran: true}
	for _, o := range outcomes {
		g.Decision.Statements = append(g.Decision.Statements, rules.StatementResult{Outcome: o})
		if o == rules.OutcomeTrue {
			g.Decision.Allow = true
		}
	}
	g.Outcome = OutcomeOf(g.Decision)
	return g
}

func failed(line, col int, desc string) Got {
	return Got{Issues: rules.Issues{
		{Severity: rules.SeverityWarning, Description: "Unused function: f."},
		{Severity: rules.SeverityError, Position: &rules.Position{Line: line, Column: col}, Description: desc},
	}}
}

// TestCompare checks the verdicts the acceptance counts, on results made by hand: the engine
// behind them is not built yet.
func TestCompare(t *testing.T) {
	allowExp := Expected{Outcome: Allow}
	denyExp := Expected{Outcome: Deny, Reason: "false"}
	errExp := Expected{Outcome: Error, ErrorKind: "evaluation", Message: "Error: firestore.rules line [5], column [21]. Null value error."}
	compileExp := Expected{Outcome: Error, ErrorKind: "compile", Message: "WARNING L3:1 x\nERROR L5:35 Invalid regular expression pattern. Pattern: a(?=b).*."}
	tooLarge := Expected{Outcome: Error, ErrorKind: "request", Message: "400 INVALID_ARGUMENT Total size of sources must be smaller than 262144."}
	for _, c := range []struct {
		name                     string
		exp                      Expected
		higher                   *Expected
		excluded                 string
		got                      Got
		agree, finer, permissive bool
	}{
		{"allow", allowExp, nil, "", evaluated(rules.OutcomeFalse, rules.OutcomeTrue), true, true, false},
		{"deny", denyExp, nil, "", evaluated(rules.OutcomeFalse), true, true, false},
		{"no statement", denyExp, nil, "", evaluated(), true, true, false},
		{"error as deny", errExp, nil, "", evaluated(rules.OutcomeFalse), true, false, false},
		{"error", errExp, nil, "", evaluated(rules.OutcomeFalse, rules.OutcomeError), true, true, false},
		{"denied what was allowed", allowExp, nil, "", evaluated(rules.OutcomeError), false, false, false},
		{"allowed what was denied", denyExp, nil, "", evaluated(rules.OutcomeTrue), false, false, true},
		{"allowed what a higher source allowed", denyExp, &allowExp, "", evaluated(rules.OutcomeTrue), false, false, false},
		{"allowed an excluded case", errExp, nil, "Test API artefact", evaluated(rules.OutcomeTrue), false, false, false},
		{"compile error", compileExp, nil, "", failed(5, 35, "Invalid regular expression pattern. Pattern: a(?=b).*."), true, true, false},
		{"compile error, other message", compileExp, nil, "", failed(5, 35, "Bad pattern."), true, false, false},
		{"compile error elsewhere", compileExp, nil, "", failed(5, 34, "Invalid regular expression pattern. Pattern: a(?=b).*."), false, false, false},
		{"compiles what failed", compileExp, nil, "", evaluated(rules.OutcomeFalse), false, false, false},
		{"compiles and allows what failed", compileExp, nil, "", evaluated(rules.OutcomeTrue), false, false, true},
		{"too large", tooLarge, nil, "", failed(1, 1, "Rules file is too large."), true, true, false},
		{"does not compile", allowExp, nil, "", failed(1, 1, "Unexpected 'x'."), false, false, false},
		{"stub", allowExp, nil, "", Got{Panic: "S1-C: Compile"}, false, false, false},
	} {
		res := Result{Case: &Case{ID: c.name, Expected: c.exp, Higher: c.higher}, Excluded: c.excluded, Got: c.got}
		res.compare()
		if res.Agree != c.agree || res.Finer != c.finer || res.Permissive != c.permissive {
			t.Errorf("%s: agree %v, finer %v, permissive %v (%s); want %v, %v, %v",
				c.name, res.Agree, res.Finer, res.Permissive, res.Why, c.agree, c.finer, c.permissive)
		}
		if (!res.Agree || !res.Finer) && res.Why == "" {
			t.Errorf("%s: no reason given", c.name)
		}
	}
}

func TestReport(t *testing.T) {
	var rep Report
	add := func(p Provenance, src, cat string, excluded string, agree, permissive bool) {
		rep.Add(Result{Case: &Case{ID: src + "/" + cat, Provenance: p, Source: src, Category: cat},
			Excluded: excluded, Agree: agree, Finer: agree, Permissive: permissive, Why: "x"})
	}
	add(TestAPI, "aruma", "strings", "", true, false)
	add(TestAPI, "aruma", "strings", "", false, true)
	add(TestAPI, "pyric", "sets", "", true, false)
	add(TestAPI, "pyric", "sets", "Test API artefact: debug()", false, false)
	add(Emulator, "hidane", "rules", "", true, false)

	byProv := rep.ByProvenance()
	if tl := byProv[TestAPI]; tl.Cases != 4 || tl.Excluded != 1 || tl.Counted != 3 || tl.Agree != 2 || tl.Permissive != 1 {
		t.Errorf("Test API tally %+v", tl)
	}
	if r := byProv[Emulator].Ratio(); r != 100 {
		t.Errorf("emulator ratio %v", r)
	}
	table := rep.Table()
	for _, want := range []string{"| rules-test-api | aruma | 2 | 0 | 2 | 1 | 50.0% | 1 | 1 |", "| **rules-test-api** | **all** | 4 | 1 | 3 | 2 | 66.7% |"} {
		if !strings.Contains(table, want) {
			t.Errorf("table lacks %q:\n%s", want, table)
		}
	}
	if cats := rep.CategoryTable(false); !strings.Contains(cats, "strings") || strings.Contains(cats, "sets") {
		t.Errorf("categories that disagree:\n%s", cats)
	}
	if d := rep.Disagreements(0); len(d) != 1 || !d[0].Permissive {
		t.Errorf("disagreements %v", d)
	}
	if l := rep.ExcludedList(); !strings.Contains(l, "debug() (1): `pyric/sets`") {
		t.Errorf("excluded list %q", l)
	}
	if rep.Stub() != "" {
		t.Error("a stub without a panic")
	}
}

// TestRunStub runs real cases through the contract's stubs: every result says so, none
// panics, and none agrees.
func TestRunStub(t *testing.T) {
	set := loadOracle(t)
	r := NewRunner()
	n := 0
	for _, c := range set.Cases {
		if c.Provenance != TestAPI || c.Category != "encoding" {
			continue
		}
		res := r.Run(context.Background(), c)
		if res.Agree || (res.Got.Panic == "" && !res.Got.Compiled) {
			t.Skip("Compile is no longer the stub")
		}
		if !res.Stub() || !strings.Contains(res.Why, "S1-") {
			t.Errorf("%s: %+v", c.ID, res)
		}
		n++
	}
	if n == 0 {
		t.Fatal("no encoding case")
	}
}
