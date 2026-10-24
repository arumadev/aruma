package media

import (
	"net/http"
	"strings"
	"testing"
)

// TestHeadersByModeAndType is ADR-022 §10.1-§10.5's table: every type a browser could run or
// render, on every mode.
func TestHeadersByModeAndType(t *testing.T) {
	types := []struct {
		name, ct, wantCT, csp string
		attachment            bool // whatever is stored
	}{
		{"evil.html", "text/html", "text/html", sandboxCSP, false},
		{"evil.svg", "image/svg+xml", "image/svg+xml", sandboxCSP, false},
		{"evil.js", "text/javascript", "text/javascript", sandboxCSP, false},
		{"feed.xml", "application/xml", "application/xml", sandboxCSP, false},
		{"photo.png", "image/png", "image/png", sandboxCSP, false},
		{"report.pdf", "application/pdf", "application/pdf", sandboxCSP, true},
		{"REPORT.PDF", "Application/PDF; x=1", "Application/PDF; x=1", sandboxCSP, true},
		{"clip.mp4", "video/mp4", "video/mp4", mediaCSP, false},
		{"song.mp3", "audio/mpeg", "audio/mpeg", mediaCSP, false},
		{"x.html", "", octetStream, sandboxCSP, false}, // never sniffed from the name or bytes
		{"blob", "application/x-unknown", "application/x-unknown", sandboxCSP, false},
	}
	modes := []struct {
		name string
		m    Mode
		corp string
	}{
		{"rules", RulesDecided, "cross-origin"},
		{"token", Token, "cross-origin"},
		{"gcs", GCS, "cross-origin"},
		{"console", Console, "same-origin"},
	}
	data := []byte("<html><script>alert(document.cookie)</script></html>")
	for _, ty := range types {
		for _, mo := range modes {
			t.Run(ty.name+"/"+mo.name, func(t *testing.T) {
				it := object("dir/"+ty.name, ty.ct, data)
				it.ContentDisposition = "inline; filename*=utf-8''" + ty.name
				rec, err := get(t, newGet(), it, data, mo.m, generous)
				if err != nil || rec.Code != http.StatusOK {
					t.Fatalf("Serve: %d %v", rec.Code, err)
				}
				h := rec.Header()
				want := map[string]string{
					"Content-Type":                   ty.wantCT,
					"X-Content-Type-Options":         "nosniff",
					"Content-Security-Policy":        ty.csp,
					"Cross-Origin-Resource-Policy":   mo.corp,
					"ETag":                           it.ETag,
					"Last-Modified":                  "Sat, 24 Oct 2026 09:30:15 GMT",
					"Accept-Ranges":                  "bytes",
					"Content-Length":                 "52",
					"x-goog-generation":              "1784647629568292",
					"x-goog-metageneration":          "3",
					"x-goog-stored-content-encoding": "identity",
				}
				disp := "inline; filename*=utf-8''" + ty.name
				if ty.attachment || mo.m == Console {
					disp = "attachment; filename*=utf-8''" + ty.name
				}
				want["Content-Disposition"] = disp
				for k, v := range want {
					if got := field(h, k); got != v {
						t.Errorf("%s = %q, want %q", k, got, v)
					}
				}
				if got := len(h["x-goog-hash"]) + len(h["X-Goog-Hash"]); got != 1 {
					t.Errorf("x-goog-hash headers: %d", got)
				}
				if _, ok := h["x-goog-stored-content-length"]; ok != (mo.m == GCS) {
					t.Errorf("x-goog-stored-content-length on %s: %v", mo.name, ok)
				}
				for k, vs := range h {
					for _, v := range vs {
						if v == "" {
							t.Errorf("empty header %s", k)
						}
					}
				}
				if rec.Body.String() != string(data) {
					t.Errorf("body %q", rec.Body)
				}
			})
		}
	}
}

func TestContentDisposition(t *testing.T) {
	cases := []struct {
		name, stored, ct string
		m                Mode
		want             string
	}{
		{"a/plain.txt", "inline; filename*=utf-8''plain.txt", "text/plain", Token, "inline; filename*=utf-8''plain.txt"},
		{"a/plain.txt", "inline", "text/plain", RulesDecided, "inline"},
		// Stored by none (a GCS upload): attachment with the last segment (§10.4, CT).
		{"a/plain.txt", "", "text/plain", RulesDecided, "attachment; filename*=utf-8''plain.txt"},
		{"a/plain.txt", "", "text/plain", GCS, "attachment; filename*=utf-8''plain.txt"},
		{"dir/", "", "text/plain", GCS, "attachment"},
		// PY-RR's published case, escaped as core stores it.
		{"x/naïve 'q' 1+1%;x.txt", "", "text/plain", Token, "attachment; filename*=utf-8''na%C3%AFve%20%27q%27%201+1%25%3Bx.txt"},
		// A PDF is attachment, keeping what the client named it.
		{"r.pdf", "inline; filename=\"Q3.pdf\"", "application/pdf", Token, "attachment; filename=\"Q3.pdf\""},
		{"r.pdf", "inline", "application/pdf", Token, "attachment; filename*=utf-8''r.pdf"},
		{"r.pdf", "", "application/pdf", GCS, "attachment; filename*=utf-8''r.pdf"},
		{"r.html", "inline; filename=r.html", "text/html", Console, "attachment; filename=r.html"},
		// A value with CR or LF never reaches a header: the default instead.
		{"a.txt", "inline\r\nSet-Cookie: x=1", "text/plain", Token, "attachment; filename*=utf-8''a.txt"},
		{"a.txt", "inline\nX: y", "text/plain", GCS, "attachment; filename*=utf-8''a.txt"},
		{"a.txt", "inline\x00", "text/plain", GCS, "attachment; filename*=utf-8''a.txt"},
	}
	for _, c := range cases {
		it := Item{Name: c.name, ContentDisposition: c.stored, ContentType: c.ct}
		if got := disposition(it, c.m, mediaType(c.ct) == "application/pdf"); got != c.want {
			t.Errorf("%q %q mode %d: %q, want %q", c.name, c.stored, c.m, got, c.want)
		}
	}
}

// TestFilenameMatchesStoredDefault holds filenameParam to core.defaultDisposition's escaping
// on PY-RR storedObjects, the one case production shows: + kept, ' as %27.
func TestFilenameMatchesStoredDefault(t *testing.T) {
	const stored = "inline; filename*=utf-8''na%C3%AFve%20%27q%27%201+1%25%3Bx.txt" // PY-RR
	if got := "inline; " + filenameParam("naïve 'q' 1+1%;x.txt"); got != stored {
		t.Errorf("%q, want %q", got, stored)
	}
	if got := filenameParam("plain.txt"); got != "filename*=utf-8''plain.txt" {
		t.Errorf("%q", got)
	}
	for c := range 256 {
		esc := filenameParam(string([]byte{byte(c)}))
		if strings.ContainsAny(esc, "\r\n\"; \x00") {
			t.Errorf("byte %#x escapes to %q", c, esc)
		}
	}
}

func TestCacheControl(t *testing.T) {
	cases := []struct {
		stored string
		m      Mode
		want   string
	}{
		{"", RulesDecided, "private, max-age=0"},
		{"", Token, "private, max-age=0"},
		{"", GCS, "private, max-age=0"},
		{"public, max-age=3600", RulesDecided, "private, max-age=3600"},
		{"Public, S-MaxAge=600, max-age=60", RulesDecided, "private, max-age=60"},
		{"max-age=60, must-revalidate", RulesDecided, "private, max-age=60, must-revalidate"},
		{"private, max-age=10", RulesDecided, "private, max-age=10"},
		{"no-store", RulesDecided, "private, no-store"},
		{"public, s-maxage=600", RulesDecided, "private"},
		// The link is the credential; GCS keeps what Attrs() says.
		{"public, max-age=3600", Token, "public, max-age=3600"},
		{"public, s-maxage=600", GCS, "public, s-maxage=600"},
		// The dashboard's download is never cached.
		{"public, max-age=3600", Console, "no-store"},
		{"", Console, "no-store"},
		{"public\r\nX: y", Token, "private, max-age=0"},
	}
	for _, c := range cases {
		if got := cacheControl(c.stored, c.m); got != c.want {
			t.Errorf("%q mode %d: %q, want %q", c.stored, c.m, got, c.want)
		}
	}
}

// TestInertUploads serves the files a hostile uploader would try: each reply is under sandbox
// with nosniff and its stored type, so a browser renders it with no script and no origin, and
// x.html without a type is octet-stream.
func TestInertUploads(t *testing.T) {
	files := []struct{ name, ct, body string }{
		{"evil.html", "text/html; charset=utf-8", "<script>fetch('/console/v1/projects')</script>"},
		{"evil.svg", "image/svg+xml", `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`},
		{"evil.js", "application/javascript", "navigator.serviceWorker.register(location.href)"},
		{"x.html", "", "<html><script>alert(1)</script></html>"},
		{"plain.txt", "text/plain", "<html><script>alert(1)</script></html>"},
	}
	for _, f := range files {
		for _, m := range []Mode{RulesDecided, Token, GCS, Console} {
			it := object(f.name, f.ct, []byte(f.body))
			it.ContentDisposition = "inline" // the client's own choice, no protection (F §2.2-2)
			rec, err := get(t, newGet("Sec-Fetch-Site", "none", "Sec-Fetch-Dest", "document"), it, []byte(f.body), m, generous)
			if err != nil || rec.Code != http.StatusOK {
				t.Fatalf("%s: %d %v", f.name, rec.Code, err)
			}
			h := rec.Header()
			csp := h.Values("Content-Security-Policy")
			if len(csp) != 1 || !strings.HasSuffix(csp[0], "; sandbox") || !strings.HasPrefix(csp[0], "default-src 'none'") {
				t.Errorf("%s: CSP %q", f.name, csp)
			}
			if strings.Contains(csp[0], "allow-") {
				t.Errorf("%s: sandbox loosened: %q", f.name, csp[0])
			}
			if field(h, "X-Content-Type-Options") != "nosniff" {
				t.Errorf("%s: no nosniff", f.name)
			}
			if f.ct == "" && field(h, "Content-Type") != octetStream {
				t.Errorf("%s: sniffed as %q", f.name, field(h, "Content-Type"))
			}
		}
		// Loaded as a script or style from the dashboard's origin, or as a worker anywhere: 403.
		it := object(f.name, f.ct, []byte(f.body))
		for _, hd := range [][]string{
			{"Sec-Fetch-Site", "same-origin", "Sec-Fetch-Dest", "script"},
			{"Sec-Fetch-Site", "same-origin", "Sec-Fetch-Dest", "style"},
			{"Sec-Fetch-Site", "cross-site", "Sec-Fetch-Dest", "worker"},
			{"Service-Worker", "script"},
		} {
			rec, _ := get(t, newGet(hd...), it, []byte(f.body), Token, generous)
			if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "script") {
				t.Errorf("%s %v: %d %q", f.name, hd, rec.Code, rec.Body)
			}
		}
	}
}

// TestStoredValuesWithControlCharacters: a stored value with a control character never
// reaches a header.
func TestStoredValuesWithControlCharacters(t *testing.T) {
	it := object("a", "text/html\r\nSet-Cookie: s=1", nil)
	it.ContentLanguage = "en\nX: y"
	rec, _ := get(t, newGet(), it, nil, Token, generous)
	if got := rec.Header().Get("Content-Type"); got != octetStream {
		t.Errorf("Content-Type %q", got)
	}
	if _, ok := rec.Header()["Content-Language"]; ok {
		t.Errorf("Content-Language %q", rec.Header().Get("Content-Language"))
	}
}

func TestStoredEncodingHeader(t *testing.T) {
	for enc, want := range map[string]string{"": "identity", "identity": "identity", "gzip": "gzip", "GZIP": "gzip", "br": "br"} {
		if got := storedEncoding(enc); got != want {
			t.Errorf("%q: %q, want %q", enc, got, want)
		}
	}
}
