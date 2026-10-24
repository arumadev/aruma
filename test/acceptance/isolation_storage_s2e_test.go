package acceptance

import "testing"

// S2-E's Storage isolation cases (ADR-022 §3); the file is S2-E's alone.

func TestIsolationStorageRuleset(t *testing.T) {
	t.Skip("S2-E: a project's Storage ruleset is never applied to another project's bucket")
}
