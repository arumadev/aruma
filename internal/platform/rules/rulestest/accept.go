package rulestest

import (
	"fmt"
	"sort"
	"strings"

	"github.com/arumadev/aruma/internal/platform/rules"
)

// Anchor is the provenance of the anchors: cases written here, not recorded, whose verdict the
// oracle cannot give because every source that runs them is an artefact there (the Test API and
// the Storage emulator apply no lookup limit). Their expectation comes from the documented limit
// and, for Storage, from deployed Storage (A-oracle-findings §1). They are not in a Set's tally:
// each must agree, whatever the ratio.
const Anchor Provenance = "anchor"

// Anchors returns the lookup-limit anchors: Firestore allows ten distinct documents in one
// evaluation and denies the eleventh; Storage allows two and denies the third
// (pyric/storage/stdlib-realstorage-p3-lookup-budget-iam-enabled/two, /three), and a document
// read again is not counted (/repeat).
func Anchors() []*Case {
	exists := func(svc rules.Service, docs []string) (string, []Mock) {
		fn := "exists"
		if svc == rules.Storage {
			fn = "firestore.exists"
		}
		var conds []string
		var mocks []Mock
		seen := map[string]bool{}
		for _, d := range docs {
			p := "/databases/(default)/documents/lim/" + d
			conds = append(conds, fn+"("+p+")")
			if !seen[p] {
				seen[p] = true
				mocks = append(mocks, Mock{Function: fn, Path: p, Result: FieldOf(true)})
			}
		}
		return strings.Join(conds, " && "), mocks
	}
	names := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("d%d", i)
		}
		return out
	}
	mk := func(id string, svc rules.Service, docs []string, allow bool) *Case {
		cond, mocks := exists(svc, docs)
		c := &Case{ID: "anchor/" + id, Category: "lookups", Service: svc, Source: "aruma", Provenance: Anchor,
			Expression: cond, Mocks: mocks, Expected: Expected{Outcome: Deny, Reason: "false"}}
		if allow {
			c.Expected = Expected{Outcome: Allow}
		}
		auth := FieldOf(map[string]any{"uid": "alice", "token": map[string]any{"sub": "alice"}})
		when := FieldOf("2026-01-15T10:20:30.123Z")
		if svc == rules.Storage {
			c.Ruleset = "rules_version = '2';\nservice firebase.storage {\n  match /b/{bucket}/o {\n    match /lim/{file} {\n      allow get: if " + cond + ";\n    }\n  }\n}\n"
			c.Request = &Request{Method: "get", Path: "/b/demo-oracle.appspot.com/o/lim/f.txt", Auth: auth, Time: when}
		} else {
			c.Ruleset = "rules_version = '2';\nservice cloud.firestore {\n  match /databases/{database}/documents {\n    match /lim/{doc} {\n      allow get: if " + cond + ";\n    }\n  }\n}\n"
			c.Request = &Request{Method: "get", Path: "/databases/(default)/documents/lim/doc", Auth: auth, Time: when}
		}
		return c
	}
	return []*Case{
		mk("firestore/10", rules.Firestore, names(10), true),
		mk("firestore/11", rules.Firestore, names(11), false),
		mk("firestore/20", rules.Firestore, names(20), false),
		mk("storage/2", rules.Storage, names(2), true),
		mk("storage/3", rules.Storage, names(3), false),
		mk("storage/repeat", rules.Storage, []string{"d0", "d0", "d0"}, true),
		mk("storage/11", rules.Storage, names(11), false),
	}
}

// Gate decides whether the oracle tests only report. They report while Compile is the
// contract's stub, which the probe (Runner.Probe) says and every result confirms, or when asked
// (ARUMA_RULES_ORACLE=report). A probe that panics otherwise, or a stub on the probe that some
// case gets past, is an error: the tests fail rather than report.
func Gate(probeStub string, probeErr error, rep *Report, asked bool) (reportOnly bool, why string, err error) {
	switch {
	case probeErr != nil:
		return false, "", probeErr
	case probeStub != "" && rep != nil && rep.Stub() == "":
		return false, "", fmt.Errorf("rulestest: Compile is the stub on the probe (%q) but not on every case", probeStub)
	case probeStub == "" && rep != nil && rep.Stub() != "":
		return false, "", fmt.Errorf("rulestest: every case met the stub %q, but not the probe", rep.Stub())
	case probeStub != "":
		return true, fmt.Sprintf("Compile is the contract's stub (%q)", probeStub), nil
	case asked:
		return true, "ARUMA_RULES_ORACLE=report", nil
	}
	return false, "", nil
}

// Faults returns what fails the oracle tests in every mode: a panic that is not the contract's
// stub, a binding error, and a case not expressible for no reason Exclusion lists.
func Faults(rep *Report) []string {
	var out []string
	for _, res := range rep.Results {
		switch {
		case res.Got.Panic != "" && !res.Stub():
			out = append(out, "panic: "+res.Line())
		case strings.HasPrefix(res.Why, "binding: "):
			out = append(out, res.Line())
		case res.NotExpressible != "" && res.Excluded == "":
			out = append(out, res.Line())
		}
	}
	return out
}

// Count pins how many cases of a provenance count and how many are excluded.
type Count struct{ Counted, Excluded int }

// Counts returns how the tally differs from the pinned counts, so that a loader or binding
// change that silently stops counting cases fails. A provenance that is pinned to count cases
// and counts none fails too.
func Counts(rep *Report, want map[Provenance]Count) []string {
	got := rep.ByProvenance()
	var out []string
	provs := make([]Provenance, 0, len(want))
	for p := range want {
		provs = append(provs, p)
	}
	for p := range got {
		if _, ok := want[p]; !ok {
			provs = append(provs, p)
		}
	}
	sort.Slice(provs, func(i, j int) bool { return provs[i] < provs[j] })
	for _, p := range provs {
		w, t := want[p], got[p]
		if t.Counted != w.Counted || t.Excluded != w.Excluded {
			out = append(out, fmt.Sprintf("%s: %d counted and %d excluded, pinned at %d and %d", p, t.Counted, t.Excluded, w.Counted, w.Excluded))
		}
		if w.Counted > 0 && t.Counted == 0 {
			out = append(out, fmt.Sprintf("%s: nothing counts", p))
		}
	}
	return out
}

// Accept returns what fails the acceptance of ADR-021 §6: a provenance among required that
// counts nothing or agrees on fewer than threshold percent of its counted cases; a permissive
// allow; an anchor that disagrees.
func Accept(rep *Report, anchors []Result, threshold float64, required ...Provenance) []string {
	var out []string
	tallies := rep.ByProvenance()
	for _, p := range required {
		if tallies[p].Counted == 0 {
			out = append(out, fmt.Sprintf("%s: nothing counts", p))
		}
	}
	provs := make([]Provenance, 0, len(tallies))
	for p := range tallies {
		provs = append(provs, p)
	}
	sort.Slice(provs, func(i, j int) bool { return provs[i] < provs[j] })
	for _, p := range provs {
		t := tallies[p]
		if t.Counted > 0 && t.Ratio() < threshold {
			out = append(out, fmt.Sprintf("%s: %d of %d counted cases agree (%.1f%%), below %.0f%%", p, t.Agree, t.Counted, t.Ratio(), threshold))
		}
	}
	for _, res := range rep.Results {
		if res.Permissive {
			out = append(out, "more permissive than the oracle: "+res.Line())
		}
	}
	for _, res := range anchors {
		if !res.Agree || res.Permissive {
			out = append(out, "anchor: "+res.Line())
		}
	}
	return out
}
