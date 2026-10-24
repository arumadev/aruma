package acceptance

import "testing"

// S4-A's Storage isolation cases (ADR-022 §3); the file is S4-A's alone.

func TestIsolationStorageServiceAccount(t *testing.T) {
	t.Skip("S4-A: a service account does not read another project's bucket over GCS, nor rewrite into it")
}
