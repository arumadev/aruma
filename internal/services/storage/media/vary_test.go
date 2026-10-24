package media

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// A gzip object's Vary merges with the caller's: Vary: Origin from the bucket's CORS stays next
// to Accept-Encoding, each once, on the stored bytes, the decompressed ones and a 304. A replaced
// Vary would let a cache give one origin a reply made for another.
func TestVaryMerges(t *testing.T) {
	plain := []byte(strings.Repeat("hello gzip ", 100))
	it, stored := gzipObject(t, plain)
	for _, c := range []struct {
		name    string
		headers []string
		status  int
	}{
		{"gzip", []string{"Accept-Encoding", "gzip"}, http.StatusOK},
		{"identity", []string{"Accept-Encoding", "identity"}, http.StatusOK},
		{"304", []string{"Accept-Encoding", "gzip", "If-None-Match", it.ETag}, http.StatusNotModified},
	} {
		for _, m := range []Mode{RulesDecided, Token, GCS} {
			rec := httptest.NewRecorder()
			rec.Header().Add("Vary", "Origin")
			rec.Header().Add("Vary", "accept-encoding") // the caller's own, in another case: kept once
			if err := serveCatch(rec, newGet(c.headers...), it, bytes.NewReader(stored), m, generous); err != nil {
				t.Fatal(err)
			}
			if rec.Code != c.status {
				t.Fatalf("%s, mode %d: %d", c.name, m, rec.Code)
			}
			if got := rec.Header().Values("Vary"); !slices.Equal(got, []string{"Origin, accept-encoding"}) {
				t.Errorf("%s, mode %d: Vary %q", c.name, m, got)
			}
		}
	}
	// Without a Vary of the caller's, media's alone; and none on an object stored as is.
	rec, err := get(t, newGet(), it, stored, Token, generous)
	if err != nil || !slices.Equal(rec.Header().Values("Vary"), []string{"Accept-Encoding"}) {
		t.Errorf("without the caller's Vary: %q, %v", rec.Header().Values("Vary"), err)
	}
	rec = httptest.NewRecorder()
	rec.Header().Add("Vary", "Origin")
	if err := serveCatch(rec, newGet(), object("a.txt", "text/plain", plain), bytes.NewReader(plain), Token, generous); err != nil {
		t.Fatal(err)
	}
	if got := rec.Header().Values("Vary"); !slices.Equal(got, []string{"Origin"}) {
		t.Errorf("identity object: Vary %q", got)
	}
}
