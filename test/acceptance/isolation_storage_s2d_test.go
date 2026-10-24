package acceptance

import "testing"

// S2-D's Storage isolation cases (ADR-022 §3); the file is S2-D's alone.

func TestIsolationStorageUploadURL(t *testing.T) {
	t.Skip("S2-D: an upload session URL of one project does not finish an upload in another, nor on another name")
}
