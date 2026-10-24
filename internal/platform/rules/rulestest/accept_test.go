package rulestest

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/arumadev/aruma/internal/platform/rules"
)

// fakeRunner returns a Runner whose Compile is compile, for the gate's tests.
func fakeRunner(compile func(rules.Service, rules.File, rules.Limits) (*rules.Ruleset, rules.Issues)) *Runner {
	r := NewRunner()
	r.CompileFunc = compile
	return r
}

var compileError = rules.Issues{{Severity: rules.SeverityError, Position: &rules.Position{Line: 1, Column: 1}, Description: "Unexpected 'x'."}}

// TestGateFailsOnAnyOtherPanic: only a panic with the contract's prefix, on the probe and on
// every case, makes the oracle tests report; an engine with a bug on the probe fails them.
func TestGateFailsOnAnyOtherPanic(t *testing.T) {
	set := loadOracle(t)
	run := func(r *Runner) (string, error, *Report) {
		stub, err := r.Probe()
		rep := &Report{}
		for _, c := range set.Cases[:200] {
			rep.Add(r.Run(context.Background(), c))
		}
		return stub, err, rep
	}

	// The reviewer's engine: a nil-map panic on the probe, a compile error everywhere else.
	buggy := fakeRunner(func(_ rules.Service, f rules.File, _ rules.Limits) (*rules.Ruleset, rules.Issues) {
		if f.Content == probe.Content {
			var m map[string]int
			m["x"] = 1
		}
		return nil, compileError
	})
	stub, err, rep := run(buggy)
	if _, _, gerr := Gate(stub, err, rep, true); gerr == nil {
		t.Error("an engine that panics on the probe passed the gate, even asked to report")
	}
	if len(Accept(rep, nil, 98)) == 0 {
		t.Error("a 0% engine passed the acceptance")
	}

	// A stub panic on the probe that some case gets past is not the stub.
	partial := fakeRunner(func(_ rules.Service, f rules.File, _ rules.Limits) (*rules.Ruleset, rules.Issues) {
		if f.Content == probe.Content {
			panic("S1-C: Compile")
		}
		return nil, compileError
	})
	stub, err, rep = run(partial)
	if stub == "" || err != nil || rep.Stub() != "" {
		t.Fatalf("probe %q %v, report stub %q", stub, err, rep.Stub())
	}
	if _, _, gerr := Gate(stub, err, rep, false); gerr == nil {
		t.Error("a stub on the probe alone passed the gate")
	}

	// Another panic in a case is a fault in every mode.
	crash := fakeRunner(func(rules.Service, rules.File, rules.Limits) (*rules.Ruleset, rules.Issues) {
		panic("index out of range")
	})
	if _, err := crash.Probe(); err == nil {
		t.Error("a crash on the probe is not an error")
	}
	_, _, rep = run(crash)
	if len(Faults(rep)) == 0 || rep.Stub() != "" {
		t.Errorf("a crash is not a fault: %d faults, stub %q", len(Faults(rep)), rep.Stub())
	}

	// The real stub reports.
	stub, err, rep = run(NewRunner())
	if stub == "" {
		t.Skip("Compile is no longer the stub")
	}
	if only, _, gerr := Gate(stub, err, rep, false); !only || gerr != nil || len(Faults(rep)) != 0 {
		t.Errorf("the contract's stub: report only %v, %v, faults %v", only, gerr, Faults(rep))
	}
}

// perfect is what an engine that agrees with every case would make of it, except that with
// noLimit it allows every condition whose lookups are all found, however many there are.
func perfect(c *Case, noLimit bool) Got {
	exp := c.Expected
	if exp.Compile() || exp.SourceTooLarge() {
		g := Got{Tried: true}
		if line, col, desc, ok := exp.FirstError(); ok {
			g.Issues = rules.Issues{{Severity: rules.SeverityError, Position: &rules.Position{Line: line, Column: col}, Description: desc}}
		} else {
			g.Issues = compileError
		}
		return g
	}
	g := Got{Tried: true, Compiled: true, Ran: true}
	outcome := map[Outcome]rules.Outcome{Allow: rules.OutcomeTrue, Deny: rules.OutcomeFalse, Error: rules.OutcomeError}[exp.Outcome]
	if noLimit && distinctLookups(c) > lookupLimit(c.Service) && exp.Outcome != Allow {
		allFound := true
		for _, m := range c.Mocks {
			r := strings.TrimSpace(string(m.Result.Raw()))
			allFound = allFound && r != "" && r != "null" && r != "false"
		}
		if allFound {
			outcome = rules.OutcomeTrue
		}
	}
	g.Decision.Statements = []rules.StatementResult{{Outcome: outcome}}
	g.Decision.Allow = outcome == rules.OutcomeTrue
	g.Outcome = OutcomeOf(g.Decision)
	return g
}

func tally(set []*Case, noLimit bool) *Report {
	rep := &Report{}
	for _, c := range set {
		res := Result{Case: c, Excluded: Exclusion(c), Got: perfect(c, noLimit)}
		if c.Ruleset == "" {
			res.Got = Got{}
		}
		res.compare()
		rep.Add(res)
	}
	return rep
}

// TestNoLookupLimitFails is the reviewer's mutated tally: an engine that agrees with every case
// but applies no lookup limit must fail, on the emulator's cases of the Firestore limit and on
// the anchors; the same engine with the limits passes.
func TestNoLookupLimitFails(t *testing.T) {
	set := loadOracle(t)
	var anchors, anchorsNoLimit []Result
	for _, c := range Anchors() {
		for _, noLimit := range []bool{false, true} {
			res := Result{Case: c, Got: perfect(c, noLimit)}
			if noLimit && distinctLookups(c) > lookupLimit(c.Service) {
				res.Got.Decision = rules.Decision{Allow: true, Statements: []rules.StatementResult{{Outcome: rules.OutcomeTrue}}}
				res.Got.Outcome = Allow
			}
			res.compare()
			if noLimit {
				anchorsNoLimit = append(anchorsNoLimit, res)
			} else {
				anchors = append(anchors, res)
			}
		}
	}

	if f := Accept(tally(set.Cases, false), anchors, 98, TestAPI, Emulator); len(f) != 0 {
		t.Errorf("a perfect engine fails:\n%s", strings.Join(f, "\n"))
	}

	failures := Accept(tally(set.Cases, true), anchorsNoLimit, 98, TestAPI, Emulator)
	joined := strings.Join(failures, "\n")
	for _, want := range []string{"aruma/limits/exists_11 [emulator", "aruma/limits/get_11 [emulator", "anchor/firestore/11 [anchor", "anchor/storage/3 [anchor"} {
		if !strings.Contains(joined, want) {
			t.Errorf("an engine without lookup limits passes on %s:\n%s", want, joined)
		}
	}
}

// TestAnchorsBind: the anchors bind, and their mocks reach Documents.
func TestAnchorsBind(t *testing.T) {
	for _, c := range Anchors() {
		_, req, err := c.Build(rules.NewInflight(1 << 20))
		if err != nil {
			t.Fatalf("%s: %v", c.ID, err)
		}
		if Exclusion(c) != "" || len(c.Mocks) == 0 || c.Provenance != Anchor {
			t.Errorf("%s: exclusion %q, %d mocks", c.ID, Exclusion(c), len(c.Mocks))
		}
		if _, found, err := req.Documents.Get(context.Background(), Project, Segments(c.Mocks[0].Path), false); !found || err != nil {
			t.Errorf("%s: the first mock is %v, %v", c.ID, found, err)
		}
	}
}

func TestCounts(t *testing.T) {
	rep := &Report{}
	rep.Add(Result{Case: &Case{Provenance: TestAPI}, Agree: true})
	rep.Add(Result{Case: &Case{Provenance: TestAPI}, Excluded: "x"})
	if f := Counts(rep, map[Provenance]Count{TestAPI: {1, 1}}); len(f) != 0 {
		t.Errorf("pinned counts fail: %v", f)
	}
	if f := Counts(rep, map[Provenance]Count{TestAPI: {2, 0}, Emulator: {5, 0}}); len(f) != 3 {
		t.Errorf("counts that moved, and a provenance that counts nothing: %v", f)
	} else if !strings.HasSuffix(f[0], "update oracleCounts in internal/platform/rules/oracle_test.go and give the reason in the commit") {
		t.Errorf("a count that moved does not say how to update the pin: %q", f[0])
	}
	if f := Accept(rep, nil, 98, TestAPI, Emulator); len(f) != 1 || !strings.Contains(f[0], "emulator: nothing counts") {
		t.Errorf("a required provenance that counts nothing: %v", f)
	}
}

// TestNotExpressibleMustBeListed: a case the binding cannot give the engine, and that Exclusion
// does not exclude, is a fault rather than a silent exclusion.
func TestNotExpressibleMustBeListed(t *testing.T) {
	c := testAPICase(`{"method": "get", "path": "databases/(default)/documents/c/d"}`, "")
	c.Ruleset = "rules_version = '2';"
	res := NewRunner().Run(context.Background(), c)
	if res.NotExpressible == "" || res.Excluded != "" || res.Agree || len(Faults(&Report{Results: []Result{res}})) != 1 {
		t.Errorf("%+v", res)
	}
	var ne *NotExpressibleError
	listed := testAPICase(`{"method": "write", "path": "/databases/(default)/documents/c/d"}`, "")
	listed.Ruleset = c.Ruleset
	if _, _, err := listed.Build(nil); !errors.As(err, &ne) {
		t.Fatal(err)
	}
	if res := NewRunner().Run(context.Background(), listed); res.Excluded == "" || len(Faults(&Report{Results: []Result{res}})) != 0 {
		t.Errorf("a listed exclusion is a fault: %+v", res)
	}
}
