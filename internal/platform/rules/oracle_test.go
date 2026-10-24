package rules_test

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/arumadev/aruma/internal/platform/rules/rulestest"
)

// The oracle tests run the development oracle of test/oracle/rules (S0-A) through the public
// contract (ADR-021 §6). They print the agreement by provenance and source:
//
//	go test ./internal/platform/rules -run 'TestOracle|TestGrammar' -v
//
// The acceptance is 98% or more of the counted cases of each provenance, and no allow that the
// case's source and every source above it denied. While Compile is the contract's stub, the
// tests report 0% and pass; once it is not, they fail below the threshold.
// ARUMA_RULES_ORACLE=report keeps them reporting while the engine is being brought up.
// The held-out set is not on this branch and the reader never reads it; S1-E measures it once,
// in heldout_test.go.

// oracleThreshold is the acceptance, in percent of the counted cases of each provenance.
const oracleThreshold = 98.0

var (
	oracleOnce sync.Once
	oracleSet  *rulestest.Set
	oracleErr  error
)

// loadOracle reads the development oracle once for the package's tests.
func loadOracle(tb testing.TB) *rulestest.Set {
	tb.Helper()
	oracleOnce.Do(func() {
		root, err := rulestest.Root()
		if err != nil {
			oracleErr = err
			return
		}
		oracleSet, oracleErr = rulestest.Load(root)
	})
	if oracleErr != nil {
		tb.Fatal(oracleErr)
	}
	return oracleSet
}

// oracleCounts pins how many cases of each provenance count and how many are excluded, so that
// a change to the oracle, the reader or the binding that stops counting cases fails here. A
// change of the oracle (S0-A) updates them, with the reason in its commit.
var oracleCounts = map[rulestest.Provenance]rulestest.Count{
	// Deployed: S7-R's Firestore probes (production/deployed-firestore.json) count; their facts
	// (the order of values()) and Pyric's Storage records, with no request to replay, do not.
	rulestest.Deployed: {Counted: 87, Excluded: 398},
	rulestest.TestAPI:  {Counted: 3643, Excluded: 55},
	rulestest.Emulator: {Counted: 1985, Excluded: 49},
}

// gate decides whether an oracle test only reports, and fails it when the probe or the results
// say the engine is broken rather than the contract's stub (rulestest.Gate).
func gate(t *testing.T, rep *rulestest.Report) bool {
	t.Helper()
	stub, err := rulestest.NewRunner().Probe()
	only, why, err := rulestest.Gate(stub, err, rep, os.Getenv("ARUMA_RULES_ORACLE") == "report")
	if err != nil {
		t.Fatal(err)
	}
	if only {
		t.Logf("%s: reporting only", why)
	}
	return only
}

func TestOracle(t *testing.T) {
	set := loadOracle(t)
	runner := rulestest.NewRunner()
	var rep rulestest.Report
	ctx := context.Background()
	for _, c := range set.Cases {
		rep.Add(runner.Run(ctx, c))
	}
	var anchors []rulestest.Result
	for _, c := range rulestest.Anchors() {
		anchors = append(anchors, runner.Run(ctx, c))
	}

	t.Logf("agreement with the development oracle, %d cases from %d files (ratio = agree / counted):\n%s",
		len(set.Cases), len(set.Files), rep.Table())
	if s := rep.Stub(); s != "" {
		t.Logf("every case met the stub %q, so every ratio is 0%%", s)
	} else {
		t.Logf("by category, where a counted case disagrees:\n%s", rep.CategoryTable(false))
	}
	t.Logf("excluded, by reason (run, not counted):\n%s", rep.ExcludedList())
	dis := rep.Disagreements(0)
	if rep.Stub() == "" && len(dis) > 0 {
		shown := dis
		if len(shown) > 60 {
			shown = shown[:60]
		}
		for _, res := range shown {
			t.Log(res.Line())
		}
		if len(dis) > len(shown) {
			t.Logf("… and %d more disagreements", len(dis)-len(shown))
		}
	}

	// What fails in every mode: the counts, panics other than the stub's, binding faults.
	for _, f := range rulestest.Counts(&rep, oracleCounts) {
		t.Error(f)
	}
	for _, f := range rulestest.Faults(&rep) {
		t.Error(f)
	}
	if gate(t, &rep) {
		return
	}
	for _, f := range rulestest.Accept(&rep, anchors, oracleThreshold, rulestest.Deployed, rulestest.TestAPI, rulestest.Emulator) {
		t.Error(f)
	}
}
