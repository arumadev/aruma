package acceptance

import "testing"

// S2-C's Storage isolation cases (ADR-022 §3); the file is S2-C's alone.

func TestIsolationStorageDownloadToken(t *testing.T) {
	t.Skip("S2-C: an object's download token does not open the object of the same name in another project")
}

func TestIsolationStoragePageToken(t *testing.T) {
	t.Skip("S2-C: a list page token of one bucket is 400 on another bucket")
}
