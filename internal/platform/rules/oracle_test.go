package rules_test

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/arumadev/aruma/internal/platform/rules"
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

// stub returns the panic of the contract's Compile stub, or "" once Compile is built.
func stub() (msg string) {
	defer func() {
		if p := recover(); p != nil {
			msg = fmt.Sprint(p)
		}
	}()
	rules.Compile(rules.Firestore, rules.File{Name: "firestore.rules", Content: "service cloud.firestore {}"}, rules.DefaultLimits())
	return ""
}

// reportOnly says whether the oracle tests only report: while Compile is a stub, or when asked.
func reportOnly(t *testing.T) bool {
	t.Helper()
	if s := stub(); s != "" {
		t.Logf("Compile is the contract's stub (%q): reporting only", s)
		return true
	}
	if os.Getenv("ARUMA_RULES_ORACLE") == "report" {
		t.Log("ARUMA_RULES_ORACLE=report: reporting only")
		return true
	}
	return false
}

func TestOracle(t *testing.T) {
	set := loadOracle(t)
	runner := rulestest.NewRunner()
	var rep rulestest.Report
	ctx := context.Background()
	for _, c := range set.Cases {
		rep.Add(runner.Run(ctx, c))
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

	if reportOnly(t) {
		return
	}
	for p, tally := range rep.ByProvenance() {
		if tally.Counted > 0 && tally.Ratio() < oracleThreshold {
			t.Errorf("%s: %d of %d counted cases agree (%.1f%%), below %.0f%%", p, tally.Agree, tally.Counted, tally.Ratio(), oracleThreshold)
		}
	}
	for _, res := range rep.Results {
		if res.Permissive {
			t.Errorf("more permissive than the oracle: %s", res.Line())
		}
	}
}
