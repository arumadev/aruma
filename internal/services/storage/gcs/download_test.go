package gcs

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/arumadev/aruma/internal/services/storage/core"
	"github.com/arumadev/aruma/internal/services/storage/wire"
)

// The media of S4-C, as Node's file.download() and Go's NewReader with WithJSONReads ask for it
// (B §6.1, §6.2).

const mediaPath = "/storage/v1/b/" + bucketA + "/o/"

func TestDownloadHeaders(t *testing.T) {
	e := newEnv(t)
	const body = "hello aruma\n"
	o := e.put(e.a, "dir/a b.txt", body, core.Patch{Fields: map[string]*string{
		core.FieldContentType: new("text/plain"), core.FieldCacheControl: new("public, max-age=3600"),
	}, Metadata: map[string]*string{"k": new("v")}})
	gen := strconv.FormatInt(o.Generation, 10)
	for _, target := range []string{
		mediaPath + "dir%2Fa%20b.txt?alt=media",                                          // Node
		mediaPath + "dir%2Fa%20b.txt?alt=media&prettyPrint=false&projection=full",        // Go, WithJSONReads
		mediaPath + "dir%2Fa%20b.txt?alt=media&generation=" + gen + "&prettyPrint=false", // Go, pinned
		"/download" + mediaPath + "dir%2Fa%20b.txt?generation=" + gen + "&alt=media",     // the mediaLink
	} {
		rec := e.do("GET", target, tokenA, nil, "Accept-Encoding", "gzip")
		h := rec.Header()
		if rec.Code != http.StatusOK || rec.Body.String() != body {
			t.Fatalf("%s: %d %q", target, rec.Code, rec.Body)
		}
		for name, want := range map[string]string{
			"Content-Type":                 "text/plain",
			"Content-Length":               strconv.Itoa(len(body)),
			"Cache-Control":                "public, max-age=3600", // as stored: Go's reader attributes agree with Attrs()
			"X-Content-Type-Options":       "nosniff",
			"Accept-Ranges":                "bytes",
			"ETag":                         wire.MediaETag(o.Generation),
			"Last-Modified":                o.Updated.UTC().Format(http.TimeFormat),
			"Cross-Origin-Resource-Policy": "cross-origin",
		} {
			if got := h.Get(name); got != want {
				t.Errorf("%s: %s = %q, want %q", target, name, got, want)
			}
		}
		// Every x-goog-* header, under the name Google spells: one x-goog-hash with no space,
		// which Node checks against by default when the stored encoding is identity.
		for name, want := range map[string]string{
			wire.GoogHash:                  "crc32c=" + crc(body) + ",md5=" + md5b64(body),
			wire.GoogGeneration:            gen,
			wire.GoogMetageneration:        "1",
			wire.GoogStoredContentEncoding: "identity",
			wire.GoogStoredContentLength:   strconv.Itoa(len(body)),
		} {
			if got := h[name]; len(got) != 1 || got[0] != want {
				t.Errorf("%s: %s = %q, want %q", target, name, got, want)
			}
		}
		if _, err := time.Parse(http.TimeFormat, h.Get("Last-Modified")); err != nil {
			t.Errorf("Last-Modified: %v", err)
		}
	}
	// HEAD: the headers without the bytes.
	rec := e.do("HEAD", mediaPath+"dir%2Fa%20b.txt?alt=media", tokenA, nil)
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 || rec.Header().Get("Content-Length") != strconv.Itoa(len(body)) {
		t.Fatalf("HEAD: %d %v", rec.Code, rec.Header())
	}
}

func TestDownloadRanges(t *testing.T) {
	e := newEnv(t)
	e.put(e.a, "r.bin", "0123456789", core.Patch{})
	for _, c := range []struct {
		rng, body, cr string
	}{
		{"bytes=2-5", "2345", "bytes 2-5/10"}, // Go: off-(off+len-1)
		{"bytes=7-", "789", "bytes 7-9/10"},   // Go: off-
		{"bytes=-3", "789", "bytes 7-9/10"},   // Node and Go: the tail
	} {
		rec := e.do("GET", mediaPath+"r.bin?alt=media", tokenA, nil, "Range", c.rng)
		if rec.Code != http.StatusPartialContent || rec.Body.String() != c.body || rec.Header().Get("Content-Range") != c.cr {
			t.Errorf("%s: %d %q %q", c.rng, rec.Code, rec.Body, rec.Header().Get("Content-Range"))
		}
	}
	rec := e.do("GET", mediaPath+"r.bin?alt=media", tokenA, nil, "Range", "bytes=20-")
	if rec.Code != http.StatusRequestedRangeNotSatisfiable || rec.Header().Get("Content-Range") != "bytes */10" {
		t.Fatalf("past the end: %d %v", rec.Code, rec.Header())
	}
}

func TestDownloadRefusals(t *testing.T) {
	e := newEnv(t)
	o := e.put(e.a, "p.txt", "data", core.Patch{})
	gen := strconv.FormatInt(o.Generation, 10)
	other := strconv.FormatInt(o.Generation+1, 10)
	for _, c := range []struct {
		query  string
		status int
		reason string
	}{
		{"&generation=" + other, 404, wire.ReasonNotFound}, // no versions: only the current one
		{"&generation=0", 400, wire.ReasonInvalid},
		{"&generation=x", 400, wire.ReasonInvalid},
		{"&ifGenerationMatch=0", 412, wire.ReasonConditionNotMet},
		{"&ifGenerationMatch=" + other, 412, wire.ReasonConditionNotMet},
		{"&ifGenerationNotMatch=" + gen, 412, wire.ReasonConditionNotMet},
		{"&ifMetagenerationMatch=2", 412, wire.ReasonConditionNotMet},
		{"&ifMetagenerationNotMatch=1", 412, wire.ReasonConditionNotMet},
		{"&ifMetagenerationMatch=x", 400, wire.ReasonInvalid},
	} {
		gcsErr(t, e.do("GET", mediaPath+"p.txt?alt=media"+c.query, tokenA, nil), c.status, c.reason)
	}
	for _, q := range []string{"&ifGenerationMatch=" + gen, "&ifGenerationNotMatch=" + other, "&ifMetagenerationMatch=1"} {
		if rec := e.do("GET", mediaPath+"p.txt?alt=media"+q, tokenA, nil); rec.Code != http.StatusOK || rec.Body.String() != "data" {
			t.Errorf("%s: %d %s", q, rec.Code, rec.Body)
		}
	}
	body := gcsErr(t, e.do("GET", mediaPath+"missing.txt?alt=media", tokenA, nil), 404, wire.ReasonNotFound)
	if body.Message != wire.MessageNoSuchObject(bucketA, "missing.txt") {
		t.Fatalf("message %q", body.Message)
	}
	// Another project's service account does not read it.
	gcsErr(t, e.do("GET", mediaPath+"p.txt?alt=media", tokenB, nil), 403, wire.ReasonForbidden)
}

// A stored gzip object is served as stored to a client that accepts gzip (Node, and Go's
// transport), and decompressed for one that does not, with x-goog-stored-content-encoding
// telling them apart (ADR-022 §10.7).
func TestDownloadGzip(t *testing.T) {
	e := newEnv(t)
	gz := gzipped(t, "plain text, compressed")
	e.put(e.a, "z.txt", gz, core.Patch{Fields: map[string]*string{
		core.FieldContentType: new("text/plain"), core.FieldContentEncoding: new("gzip"),
	}})
	rec := e.do("GET", mediaPath+"z.txt?alt=media", tokenA, nil, "Accept-Encoding", "gzip")
	if rec.Code != 200 || rec.Body.String() != gz || rec.Header().Get("Content-Encoding") != "gzip" ||
		rec.Header()[wire.GoogStoredContentEncoding][0] != "gzip" {
		t.Fatalf("as stored: %d %v", rec.Code, rec.Header())
	}
	rec = e.do("GET", mediaPath+"z.txt?alt=media", tokenA, nil)
	if rec.Code != 200 || rec.Body.String() != "plain text, compressed" || rec.Header().Get("Content-Encoding") != "" {
		t.Fatalf("decompressed: %d %v %q", rec.Code, rec.Header(), rec.Body)
	}
}

// The bytes served are counted on the surface.
func TestDownloadCountsBytes(t *testing.T) {
	e := newEnv(t)
	e.put(e.a, "c.bin", "0123456789", core.Patch{})
	out := e.svc.Metrics().Bytes.WithLabelValues("out", "gcs")
	before := counterValue(t, out)
	e.do("GET", mediaPath+"c.bin?alt=media", tokenA, nil, "Range", "bytes=0-3")
	if got := counterValue(t, out) - before; got != 4 {
		t.Fatalf("counted %v bytes, want 4", got)
	}
}

func gzipped(t *testing.T, s string) string {
	t.Helper()
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	if _, err := zw.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func counterValue(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		t.Fatal(err)
	}
	return m.GetCounter().GetValue()
}
