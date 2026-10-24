package media

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestRange is ADR-022 §10.3 on an object of 64 bytes and on an empty one.
func TestRange(t *testing.T) {
	data := []byte(strings.Repeat("0123456789abcdef", 4)) // 64 bytes
	full := string(data)
	cases := []struct {
		name    string
		size    int // 0 for the empty object
		headers []string
		status  int
		cr, len string
		body    string
	}{
		{"none", 64, nil, 200, "", "64", full},
		{"first byte", 64, []string{"Range", "bytes=0-0"}, 206, "bytes 0-0/64", "1", "0"},
		{"a-b", 64, []string{"Range", "bytes=2-5"}, 206, "bytes 2-5/64", "4", "2345"},
		{"a-", 64, []string{"Range", "bytes=60-"}, 206, "bytes 60-63/64", "4", "cdef"},
		{"suffix", 64, []string{"Range", "bytes=-3"}, 206, "bytes 61-63/64", "3", "def"},
		{"suffix longer than object", 64, []string{"Range", "bytes=-100"}, 206, "bytes 0-63/64", "64", full},
		{"end past size", 64, []string{"Range", "bytes=10-1000"}, 206, "bytes 10-63/64", "54", full[10:]},
		{"last byte", 64, []string{"Range", "bytes=63-63"}, 206, "bytes 63-63/64", "1", "f"},
		// JS's maxDownloadSizeBytes on a smaller object (requests.ts:227-230).
		{"js max download", 64, []string{"Range", "bytes=0-1048575"}, 206, "bytes 0-63/64", "64", full},
		// Android's resume (GetNetworkRequest.java:32).
		{"android resume", 64, []string{"Range", "bytes=32-"}, 206, "bytes 32-63/64", "32", full[32:]},
		{"start at size", 64, []string{"Range", "bytes=64-"}, 416, "bytes */64", "", ""},
		{"start past size", 64, []string{"Range", "bytes=100-200"}, 416, "bytes */64", "", ""},
		{"zero suffix", 64, []string{"Range", "bytes=-0"}, 416, "bytes */64", "", ""},
		// Several ranges: the whole object, never multipart/byteranges.
		{"multi", 64, []string{"Range", "bytes=0-1,4-5"}, 200, "", "64", full},
		{"multi headers", 64, []string{"Range", "bytes=0-1", "Range", "bytes=4-5"}, 200, "", "64", full},
		{"overlapping multi", 64, []string{"Range", "bytes=0-63,0-63,0-63"}, 200, "", "64", full},
		// Malformed: ignored (RFC 9110 §14.2).
		{"other unit", 64, []string{"Range", "items=0-1"}, 200, "", "64", full},
		{"reversed", 64, []string{"Range", "bytes=5-2"}, 200, "", "64", full},
		{"garbage", 64, []string{"Range", "bytes=x"}, 200, "", "64", full},
		// An empty object is 200 whatever the range, never 416 (E §4).
		{"empty none", 0, nil, 200, "", "0", ""},
		{"empty js", 0, []string{"Range", "bytes=0-1048575"}, 200, "", "0", ""},
		{"empty start past", 0, []string{"Range", "bytes=5-"}, 200, "", "0", ""},
		{"empty suffix", 0, []string{"Range", "bytes=-1"}, 200, "", "0", ""},
		{"empty zero suffix", 0, []string{"Range", "bytes=-0"}, 200, "", "0", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := data[:c.size]
			it := object("o", "application/octet-stream", d)
			for _, m := range []Mode{RulesDecided, Token, GCS, Console} {
				rec, err := get(t, newGet(c.headers...), it, d, m, generous)
				if err != nil {
					t.Fatal(err)
				}
				h := rec.Header()
				if rec.Code != c.status || field(h, "Content-Range") != c.cr {
					t.Fatalf("mode %d: %d %q, want %d %q", m, rec.Code, field(h, "Content-Range"), c.status, c.cr)
				}
				if c.status == 416 {
					if field(h, "x-goog-hash") != "" || field(h, "Cross-Origin-Resource-Policy") != "same-origin" {
						t.Errorf("416 carries media headers: %v", h)
					}
					if !strings.Contains(rec.Body.String(), `"code":416`) {
						t.Errorf("416 body %s", rec.Body)
					}
					continue
				}
				if field(h, "Content-Length") != c.len || rec.Body.String() != c.body {
					t.Errorf("mode %d: length %q body %q", m, field(h, "Content-Length"), rec.Body)
				}
				// The checksums are the whole stored object's, on a 206 too.
				if field(h, "x-goog-hash") == "" || field(h, "Accept-Ranges") != "bytes" {
					t.Errorf("mode %d: %v", m, h)
				}
			}
		})
	}
}

// TestIfRange: a Range is honoured only under the content's own ETag (§12), which follows the
// generation alone; a date, a weak tag or an older generation's tag sends the whole object.
func TestIfRange(t *testing.T) {
	data := []byte("0123456789")
	it := object("o", "text/plain", data)
	lm := it.Updated.UTC().Format(http.TimeFormat)
	older := object("o", "text/plain", data)
	older.Generation--
	cases := []struct {
		ifRange string
		status  int
	}{
		{it.ETag, 206},
		{"W/" + it.ETag, 200},
		{`"other"`, 200},
		{lm, 200},
		{"garbage", 200},
	}
	for _, c := range cases {
		rec, err := get(t, newGet("Range", "bytes=2-3", "If-Range", c.ifRange), it, data, Token, generous)
		if err != nil || rec.Code != c.status {
			t.Errorf("If-Range %q: %d %v, want %d", c.ifRange, rec.Code, err, c.status)
		}
		if c.status == 200 && rec.Body.String() != string(data) {
			t.Errorf("If-Range %q: body %q", c.ifRange, rec.Body)
		}
	}
	// The same generation after a metadata change: same ETag, so a resumed download goes on.
	patched := it
	patched.Metageneration += 5
	patched.CacheControl = "public, max-age=60"
	rec, _ := get(t, newGet("Range", "bytes=2-3", "If-Range", it.ETag), patched, data, Token, generous)
	if rec.Code != 206 || rec.Body.String() != "23" {
		t.Errorf("after PATCH: %d %q", rec.Code, rec.Body)
	}
	// An unsatisfiable range under a stale If-Range is the whole object, not a 416.
	rec, _ = get(t, newGet("Range", "bytes=100-", "If-Range", `"stale"`), it, data, Token, generous)
	if rec.Code != 200 {
		t.Errorf("stale If-Range with unsatisfiable range: %d", rec.Code)
	}
}

func TestHead(t *testing.T) {
	data := []byte("0123456789")
	it := object("o", "text/plain", data)
	req := newGet("Range", "bytes=2-3")
	req.Method = http.MethodHead
	rec, err := get(t, req, it, data, GCS, generous)
	if err != nil || rec.Code != 206 || rec.Body.Len() != 0 || rec.Header().Get("Content-Length") != "2" {
		t.Errorf("HEAD: %d %v %q %v", rec.Code, err, rec.Body, rec.Header())
	}
}

func TestNotModified(t *testing.T) {
	data := []byte("0123456789")
	it := object("o", "text/plain", data)
	cases := []struct {
		headers []string
		status  int
	}{
		{[]string{"If-None-Match", it.ETag}, 304},
		{[]string{"If-None-Match", `"a", ` + it.ETag}, 304},
		{[]string{"If-None-Match", "W/" + it.ETag}, 304},
		{[]string{"If-None-Match", "*"}, 304},
		{[]string{"If-None-Match", `"other"`}, 200},
		{[]string{"If-Modified-Since", it.Updated.Add(time.Second).Format(http.TimeFormat)}, 304},
		{[]string{"If-Modified-Since", it.Updated.Format(http.TimeFormat)}, 304},
		{[]string{"If-Modified-Since", it.Updated.Add(-time.Second).Format(http.TimeFormat)}, 200},
		{[]string{"If-Modified-Since", "garbage"}, 200},
		// If-None-Match decides alone when present.
		{[]string{"If-None-Match", `"other"`, "If-Modified-Since", it.Updated.Add(time.Hour).Format(http.TimeFormat)}, 200},
	}
	for _, c := range cases {
		rec, err := get(t, newGet(c.headers...), it, data, Token, generous)
		if err != nil || rec.Code != c.status {
			t.Errorf("%v: %d %v, want %d", c.headers, rec.Code, err, c.status)
		}
		if c.status == 304 {
			h := rec.Header()
			if rec.Body.Len() != 0 || field(h, "Content-Type") != "" || field(h, "ETag") != it.ETag {
				t.Errorf("%v: 304 %v %q", c.headers, h, rec.Body)
			}
		}
	}
}

// TestShortBody: a body that ends before the object's size is an error, so the caller cuts the
// connection instead of closing a reply that declared more.
func TestShortBody(t *testing.T) {
	it := object("o", "text/plain", []byte("0123456789"))
	_, err := get(t, newGet(), it, []byte("01234"), Token, generous)
	if err != ErrShortBody {
		t.Errorf("err %v", err)
	}
}
