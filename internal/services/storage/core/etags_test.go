package core

import (
	"encoding/json"
	"os"
	"testing"
)

// The etag the rules see (objectETag, resource.etag) is the one the surfaces send (wire.ETag),
// for the same generation and metageneration. core may not import wire (ADR-022 §1), so the
// two functions stay apart and wire's table holds both to one value (wire/etags_test.go).
func TestObjectETagMatchesWire(t *testing.T) {
	raw, err := os.ReadFile("../wire/testdata/etags.json")
	if err != nil {
		t.Fatal(err)
	}
	var table struct {
		Cases []struct {
			Generation     int64  `json:"generation"`
			Metageneration int64  `json:"metageneration"`
			ETag           string `json:"etag"`
		}
	}
	if err := json.Unmarshal(raw, &table); err != nil || len(table.Cases) == 0 {
		t.Fatalf("wire/testdata/etags.json: %v, %d cases", err, len(table.Cases))
	}
	for _, c := range table.Cases {
		if got := objectETag(c.Generation, c.Metageneration); got != c.ETag {
			t.Errorf("objectETag(%d, %d) = %q; wire.ETag gives %q", c.Generation, c.Metageneration, got, c.ETag)
		}
	}
}
