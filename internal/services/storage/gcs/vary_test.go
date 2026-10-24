package gcs

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/arumadev/aruma/internal/services/storage/core"
)

// A GCS download goes through media's send(), which merges Vary instead of replacing it (S3-C):
// a stored gzip object varies on Accept-Encoding, served as stored or decompressed, and a Vary a
// layer in front set before the handler, as a CORS layer sets Origin, stays beside it, each name
// once.
func TestDownloadKeepsBothVaryValues(t *testing.T) {
	e := newEnv(t)
	e.put(e.a, "v.txt", gzipped(t, "plain text, compressed"), core.Patch{Fields: map[string]*string{
		core.FieldContentType: new("text/plain"), core.FieldContentEncoding: new("gzip"),
	}})
	for _, c := range []struct {
		name, accept string
		before       []string
		want         string
	}{
		{"as stored", "gzip", nil, "Accept-Encoding"},
		{"decompressed", "", nil, "Accept-Encoding"},
		{"after Origin", "gzip", []string{"Origin"}, "Origin, Accept-Encoding"},
		{"decompressed after Origin", "", []string{"Origin"}, "Origin, Accept-Encoding"},
		{"after both", "gzip", []string{"origin, accept-encoding"}, "origin, accept-encoding"},
	} {
		r := httptest.NewRequest("GET", mediaPath+"v.txt?alt=media", nil)
		r.Header.Set("Authorization", "Bearer "+tokenA)
		if c.accept != "" {
			r.Header.Set("Accept-Encoding", c.accept)
		}
		rec := httptest.NewRecorder()
		if c.before != nil {
			rec.Header()["Vary"] = c.before
		}
		e.h.ServeHTTP(rec, r)
		if got := strings.Join(rec.Header().Values("Vary"), ", "); rec.Code != 200 || got != c.want {
			t.Errorf("%s: %d, Vary %q, want %q", c.name, rec.Code, got, c.want)
		}
	}
}
