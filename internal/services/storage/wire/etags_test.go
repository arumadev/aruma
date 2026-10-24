package wire

import (
	"encoding/json"
	"os"
	"testing"
)

// etagCase is one row of testdata/etags.json, the table core's objectETag is held to as well:
// core may not import wire (ADR-022 §1), so the rules' etag and the surfaces' are two functions,
// and this table keeps them one value (core/etags_test.go reads it too).
type etagCase struct {
	Generation     int64  `json:"generation"`
	Metageneration int64  `json:"metageneration"`
	ETag           string `json:"etag"`
}

func readETagCases(t *testing.T) []etagCase {
	t.Helper()
	raw, err := os.ReadFile("testdata/etags.json")
	if err != nil {
		t.Fatal(err)
	}
	var table struct{ Cases []etagCase }
	if err := json.Unmarshal(raw, &table); err != nil || len(table.Cases) == 0 {
		t.Fatalf("testdata/etags.json: %v, %d cases", err, len(table.Cases))
	}
	return table.Cases
}

func TestETagTable(t *testing.T) {
	for _, c := range readETagCases(t) {
		if got := ETag(c.Generation, c.Metageneration); got != c.ETag {
			t.Errorf("ETag(%d, %d) = %q, want %q", c.Generation, c.Metageneration, got, c.ETag)
		}
	}
}
