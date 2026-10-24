package media

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

// FuzzServe drives Serve with the stored values and request headers an attacker controls: no
// panic, no header value with a control character, a body never longer than the object, and
// the security headers on every 200 and 206.
func FuzzServe(f *testing.F) {
	f.Add("text/html", "inline", "public, max-age=60", "", "bytes=0-1", "", "gzip", "same-origin", "script")
	f.Add("", "", "", "gzip", "bytes=-5", `"x"`, "", "cross-site", "image")
	f.Add("a\r\nb", "x\ny", "public\r\n", "gzip\r\n", "bytes=0-1,2-3", "W/\"x\"", "gzip;q=0", "none", "document")
	f.Fuzz(func(t *testing.T, ct, disp, cc, enc, rng, ifRange, ae, site, dest string) {
		data := []byte("0123456789")
		it := object("dir/naïve;x.txt", ct, data)
		it.ContentDisposition, it.CacheControl, it.ContentEncoding = disp, cc, enc
		req := newGet()
		for k, v := range map[string]string{"Range": rng, "If-Range": ifRange, "Accept-Encoding": ae,
			"Sec-Fetch-Site": site, "Sec-Fetch-Dest": dest} {
			if v != "" {
				req.Header[k] = []string{v}
			}
		}
		for _, m := range []Mode{RulesDecided, Token, GCS, Console} {
			rec, _ := get(t, req, it, data, m, Limits{DecompressedBytes: 1 << 20, MinBytesPerSecond: 1})
			for k, vs := range rec.Header() {
				for _, v := range vs {
					if strings.ContainsAny(v, "\r\n\x00") {
						t.Fatalf("%s: %q", k, v)
					}
				}
			}
			if rec.Body.Len() > len(data) && !bytes.Contains(rec.Body.Bytes(), []byte(`"error"`)) {
				t.Fatalf("body of %d bytes", rec.Body.Len())
			}
			if rec.Code == http.StatusOK || rec.Code == http.StatusPartialContent {
				h := rec.Header()
				if h.Get("X-Content-Type-Options") != "nosniff" || !strings.HasSuffix(h.Get("Content-Security-Policy"), "sandbox") {
					t.Fatalf("security headers: %v", h)
				}
			}
		}
	})
}
