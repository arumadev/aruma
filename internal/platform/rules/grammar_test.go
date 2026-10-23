package rules_test

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/arumadev/aruma/internal/platform/rules"
	"github.com/arumadev/aruma/internal/platform/rules/rulestest"
)

// TestGrammar compiles every distinct rules file of the development oracle, without evaluating
// anything: whether it compiles, and for one that does not, the first error's line and column
// (counted) and its message (reported). Against Pyric's drift, the grammar is one for both
// services: a file that fails with a syntax error fails at the same place when its service
// declaration names the other service, and a file that compiles has no syntax error there.

// syntaxError matches the descriptions of the oracle's syntax errors, as opposed to the checks
// that follow parsing (call depth, recursion, let in version 1, regular expressions, globs).
var syntaxError = regexp.MustCompile(`^(token recognition error|Unexpected |Missing |missing |mismatched input|extraneous input|no viable alternative|Forward slash |The '.*' body must contain)`)

// compileCase is one distinct rules file and what the oracle says of compiling it.
type compileCase struct {
	svc      rules.Service
	src      string
	name     string
	fails    bool // the source did not compile it
	line     int  // the first error's position, when the source gives one
	col      int
	desc     string
	prov     rulestest.Provenance
	ids      []string
	conflict bool
}

// compileCases collects the distinct rules files of the oracle. Where two sources disagree on
// one file, the higher provenance decides; two cases of one provenance that disagree are marked.
func compileCases(set *rulestest.Set) []*compileCase {
	byKey := map[string]*compileCase{}
	var out []*compileCase
	for _, c := range set.Cases {
		if c.Ruleset == "" || c.Skip != "" {
			continue
		}
		exp := c.Expected
		cc := &compileCase{svc: c.Service, src: c.Ruleset, name: c.FileName(), prov: c.Provenance, ids: []string{c.ID}}
		switch {
		case exp.Compile():
			cc.fails = true
			cc.line, cc.col, cc.desc, _ = exp.FirstError()
		case exp.SourceTooLarge():
			cc.fails = true
		case exp.Outcome == rulestest.Allow || exp.Outcome == rulestest.Deny ||
			exp.Outcome == rulestest.Error && exp.ErrorKind == "evaluation":
		default:
			continue // a fact, or a request the source refused for another reason
		}
		k := string(c.Service) + "\x00" + c.Ruleset
		prev := byKey[k]
		switch {
		case prev == nil:
			byKey[k] = cc
			out = append(out, cc)
		case cc.prov.Rank() > prev.prov.Rank():
			cc.ids = append(cc.ids, prev.ids...)
			*prev = *cc
		case cc.prov.Rank() == prev.prov.Rank():
			prev.ids = append(prev.ids, c.ID)
			if prev.fails != cc.fails || prev.line != cc.line || prev.col != cc.col {
				prev.conflict = true
			}
		default:
			prev.ids = append(prev.ids, c.ID)
		}
	}
	return out
}

var serviceDecl = regexp.MustCompile(`service\s+(cloud\.firestore|firebase\.storage)`)

// otherService returns the file with its service declaration naming the other service, and the
// line of the declaration.
func otherService(src string) (string, rules.Service, int, bool) {
	loc := serviceDecl.FindStringSubmatchIndex(src)
	if loc == nil {
		return "", "", 0, false
	}
	name, svc := "firebase.storage", rules.Storage
	if src[loc[2]:loc[3]] == "firebase.storage" {
		name, svc = "cloud.firestore", rules.Firestore
	}
	line := strings.Count(src[:loc[2]], "\n") + 1
	return src[:loc[2]] + name + src[loc[3]:], svc, line, true
}

func firstError(is rules.Issues) (rules.Issue, bool) {
	for _, i := range is {
		if i.Severity == rules.SeverityError {
			return i, true
		}
	}
	return rules.Issue{}, false
}

func TestGrammar(t *testing.T) {
	set := loadOracle(t)
	cases := compileCases(set)
	sort.SliceStable(cases, func(i, j int) bool { return cases[i].ids[0] < cases[j].ids[0] })
	if s := stub(); s != "" {
		fails := 0
		for _, cc := range cases {
			if cc.fails {
				fails++
			}
		}
		t.Logf("%d distinct rules files, %d of which do not compile: 0%% (Compile is the stub %q)", len(cases), fails, s)
		reportOnly(t)
		return
	}

	runner := rulestest.NewRunner()
	var counted, agree, messages, withMessage, swapped, swappedAgree, conflicts int
	var diffs []string
	for _, cc := range cases {
		if cc.conflict {
			conflicts++
			continue
		}
		counted++
		rs, issues, panicked := runner.Compile(cc.svc, rules.File{Name: cc.name, Content: cc.src})
		first, _ := firstError(issues)
		ok := false
		switch {
		case panicked != "":
			diffs = append(diffs, cc.ids[0]+": panic: "+panicked)
		case !cc.fails && rs != nil:
			ok = true
		case !cc.fails:
			diffs = append(diffs, cc.ids[0]+": does not compile: "+first.String())
		case rs != nil:
			diffs = append(diffs, fmt.Sprintf("%s: compiles; the source's first error L%d:%d %s", cc.ids[0], cc.line, cc.col, cc.desc))
		case cc.line == 0:
			ok = true // the source gives no position, as for a file that is too large
		default:
			ok = first.Position != nil && first.Position.Line == cc.line && first.Position.Column == cc.col
			if !ok {
				diffs = append(diffs, fmt.Sprintf("%s: first error %s, the source's L%d:%d %s", cc.ids[0], first, cc.line, cc.col, cc.desc))
			}
		}
		if ok {
			agree++
		}
		if cc.fails && cc.desc != "" && rs == nil {
			withMessage++
			if first.Description == cc.desc {
				messages++
			}
		}

		// The same file for the other service.
		other, svc, declLine, found := otherService(cc.src)
		if !found || panicked != "" {
			continue
		}
		srs, sissues, _ := runner.Compile(svc, rules.File{Name: cc.name, Content: other})
		sfirst, serr := firstError(sissues)
		switch {
		case cc.fails && cc.line != 0 && syntaxError.MatchString(cc.desc):
			swapped++
			if srs == nil && serr && sfirst.Position != nil && sfirst.Position.Line == cc.line &&
				(cc.line == declLine || sfirst.Position.Column == cc.col) {
				swappedAgree++
			} else {
				diffs = append(diffs, fmt.Sprintf("%s for %s: first error %s, the syntax error L%d:%d %s", cc.ids[0], svc, sfirst, cc.line, cc.col, cc.desc))
			}
		case !cc.fails:
			swapped++
			if serr && syntaxError.MatchString(sfirst.Description) {
				diffs = append(diffs, cc.ids[0]+" for "+string(svc)+": a syntax error "+sfirst.String())
			} else {
				swappedAgree++
			}
		}
	}

	t.Logf("%d distinct rules files: %d agree on compiling and the first error's position (%.1f%%); "+
		"messages %d of %d; the other service %d of %d; %d skipped as conflicting",
		counted, agree, pct(agree, counted), messages, withMessage, swappedAgree, swapped, conflicts)
	for i, d := range diffs {
		if i == 60 {
			t.Logf("… and %d more", len(diffs)-i)
			break
		}
		t.Log(d)
	}
	if reportOnly(t) {
		return
	}
	if pct(agree, counted) < oracleThreshold {
		t.Errorf("%d of %d rules files agree (%.1f%%), below %.0f%%", agree, counted, pct(agree, counted), oracleThreshold)
	}
	if pct(swappedAgree, swapped) < oracleThreshold {
		t.Errorf("for the other service, %d of %d agree (%.1f%%), below %.0f%%", swappedAgree, swapped, pct(swappedAgree, swapped), oracleThreshold)
	}
}

func pct(n, of int) float64 {
	if of == 0 {
		return 100
	}
	return 100 * float64(n) / float64(of)
}
