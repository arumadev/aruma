package web

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

const page = `<!DOCTYPE html><html lang="ar" dir="rtl"><head>` +
	`<script src="/_next/static/chunks/main.js" async=""></script></head><body>` +
	`<script>self.__next_f=self.__next_f||[]</script><script>self.__next_f.push([1,"x"])</script>` +
	`<script>self.__next_f.push([1,"x"])</script></body></html>`

func export() fstest.MapFS {
	return fstest.MapFS{
		"index.html":                      {Data: []byte(page)},
		"projects/index.html":             {Data: []byte("<html>projects</html>")},
		"404.html":                        {Data: []byte("<html>not found</html>")},
		"_next/static/chunks/main-abc.js": {Data: []byte("console.log(1)")},
		"favicon.ico":                     {Data: []byte{0, 0, 1}},
		".DS_Store":                       {Data: []byte("junk")},
	}
}

func get(t *testing.T, h http.Handler, path string, hdr ...string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", path, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func TestServing(t *testing.T) {
	h, err := New(export())
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		path   string
		status int
		body   string
		cache  string
	}{
		{"/", 200, "__next_f", "no-store"},
		{"/projects/", 200, "projects", "no-store"},
		{"/projects/?id=chat-app", 200, "projects", "no-store"},
		{"/_next/static/chunks/main-abc.js", 200, "console.log", "public, max-age=31536000, immutable"},
		{"/favicon.ico", 200, "", "public, max-age=3600"},
		{"/nope/", 404, "not found", "no-store"},
		{"/.DS_Store", 404, "not found", ""},
		{"/../../etc/passwd", 404, "not found", ""},
	}
	for _, c := range cases {
		rec := get(t, h, c.path)
		if rec.Code != c.status || !strings.Contains(rec.Body.String(), c.body) {
			t.Errorf("%s: %d %q", c.path, rec.Code, rec.Body.String())
		}
		if c.cache != "" && rec.Header().Get("Cache-Control") != c.cache {
			t.Errorf("%s: Cache-Control %q, want %q", c.path, rec.Header().Get("Cache-Control"), c.cache)
		}
	}
	if rec := get(t, h, "/projects?id=chat-app"); rec.Code != 301 || rec.Header().Get("Location") != "/projects/?id=chat-app" {
		t.Errorf("redirect: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	r := httptest.NewRequest("POST", "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != 405 {
		t.Errorf("POST: %d", rec.Code)
	}
}

func TestCSPHashesInlineScripts(t *testing.T) {
	h, _ := New(export())
	csp := get(t, h, "/").Header().Get("Content-Security-Policy")
	hash := func(s string) string {
		sum := sha256.Sum256([]byte(s))
		return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	}
	for _, want := range []string{hash("self.__next_f=self.__next_f||[]"), hash(`self.__next_f.push([1,"x"])`), "frame-ancestors 'none'", "object-src 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP lacks %s: %s", want, csp)
		}
	}
	if strings.Contains(csp, "unsafe-inline") || strings.Count(csp, "sha256-") != 2 {
		t.Errorf("CSP: %s", csp)
	}
	if get(t, h, "/_next/static/chunks/main-abc.js").Header().Get("Content-Security-Policy") != "" {
		t.Error("non-HTML files need no page policy")
	}
}

// directives splits a policy into its directives' sources, by name.
func directives(t *testing.T, csp string) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for d := range strings.SplitSeq(csp, ";") {
		f := strings.Fields(d)
		if len(f) == 0 {
			t.Fatalf("empty directive in %q", csp)
		}
		if _, dup := out[f[0]]; dup {
			t.Fatalf("%s twice in %q", f[0], csp)
		}
		out[f[0]] = f[1:]
	}
	return out
}

// TestCSPCodeOnlyFromStaticDir pins the dashboard's policy (ADR-022 §10.1): scripts,
// stylesheets and fonts load from <host>/_next/static/ and the page's hashes alone. Never
// 'self', which would let an object Storage serves on this origin run as the dashboard's
// script: on plain http no Sec-Fetch-* header lets media refuse it.
func TestCSPCodeOnlyFromStaticDir(t *testing.T) {
	fsys := export()
	style := "'sha256-38RhXrc7EdReTKsOm23ZPOCUgniTUUcjky8QOOrQx6o='"
	fsys["csp.json"] = &fstest.MapFile{Data: []byte(`{"styleSrcElem":["` + style + `"]}`)}
	h, err := New(fsys)
	if err != nil {
		t.Fatal(err)
	}
	hash := func(s string) string {
		sum := sha256.Sum256([]byte(s))
		return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	}
	scripts := hash("self.__next_f=self.__next_f||[]") + " " + hash(`self.__next_f.push([1,"x"])`)
	cases := []struct {
		host, static string
	}{
		{"console.example.com", "console.example.com/_next/static/"},
		{"127.0.0.1:9360", "127.0.0.1:9360/_next/static/"},
		{"localhost", "localhost/_next/static/"},
		{"xn--mgbh0fb.example:8443", "xn--mgbh0fb.example:8443/_next/static/"},
		// A Host no host-source can name gets the page that says so ("" below): an IPv6
		// literal, an underscore, an empty label or a trailing dot would be an invalid source,
		// which browsers drop, and a space or a ';' would add a source or a directive.
		{"[::1]:9360", ""},
		{"a_b.example", ""},
		{"a..example", ""},
		{"a.example.", ""},
		{"evil.example; script-src *", ""},
		{"a.example *", ""},
		{"", ""},
		{"a.example/x", ""},
		{"'self'", ""},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.Host = c.host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		csp := rec.Header().Get("Content-Security-Policy")
		if c.static == "" {
			body := rec.Body.String()
			if rec.Code != 400 || csp != "default-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'" ||
				rec.Header().Get("Cache-Control") != "no-store" || strings.Contains(body, "<script") ||
				!strings.Contains(body, "through a host name") || !strings.Contains(body, "افتح اللوحة باسم مضيف") {
				t.Errorf("Host %q: %d %q %q %s", c.host, rec.Code, csp, rec.Header().Get("Cache-Control"), body)
			}
			continue
		}
		want := "default-src 'none'; script-src " + c.static + " " + scripts + "; style-src " + c.static + " " + style +
			"; font-src " + c.static + "; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; " +
			"form-action 'self'; frame-ancestors 'none'"
		if csp != want {
			t.Errorf("Host %q:\n got %s\nwant %s", c.host, csp, want)
			continue
		}
		d := directives(t, csp)
		for _, name := range []string{"script-src", "style-src", "font-src"} {
			for _, src := range d[name] {
				if src == c.static || (name != "font-src" && strings.HasPrefix(src, "'sha256-")) {
					continue
				}
				t.Errorf("Host %q: %s allows %s", c.host, name, src)
			}
		}
		for _, name := range []string{"default-src", "object-src", "base-uri", "frame-ancestors"} {
			if got := strings.Join(d[name], " "); got != "'none'" {
				t.Errorf("Host %q: %s %s", c.host, name, got)
			}
		}
	}
}

// TestPagesNeverCached: a page's policy names the request's Host, so no cache may keep a page
// and serve it under another Host; the build's hashed files stay cacheable.
func TestPagesNeverCached(t *testing.T) {
	h, _ := New(export())
	for _, c := range []struct{ path, host, cache string }{
		{"/", "a.example", "no-store"},
		{"/projects/", "b.example:8080", "no-store"},
		{"/nope/", "a.example", "no-store"},
		{"/", "[::1]", "no-store"},
		{"/_next/static/chunks/main-abc.js", "a.example", "public, max-age=31536000, immutable"},
		{"/_next/static/chunks/main-abc.js", "[::1]", "public, max-age=31536000, immutable"},
	} {
		r := httptest.NewRequest("GET", c.path, nil)
		r.Host = c.host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if got := rec.Header().Get("Cache-Control"); got != c.cache {
			t.Errorf("%s on %s: Cache-Control %q, want %q", c.path, c.host, got, c.cache)
		}
	}
}

// TestStaticDirNeverRedirects: CSP checks a script's path before a redirect only, so a path
// under /_next/static/ (raw or decoded) is one of the build's files there, named exactly, or a
// plain 404. Never a redirect, and never a page or a file elsewhere in the export.
func TestStaticDirNeverRedirects(t *testing.T) {
	h, _ := New(export())
	if rec := get(t, h, "/_next/static/chunks/main-abc.js"); rec.Code != 200 || rec.Body.String() != "console.log(1)" {
		t.Fatalf("the build's file: %d %q", rec.Code, rec.Body)
	}
	for _, p := range []string{
		"/_next/static/..%2f..%2fprojects",
		"/_next/static/..%2f..%2fprojects/",
		"/_next/static/..%2F..%2Fv0/b/x/o/e.js",
		"/_next/static/%2e%2e/%2e%2e/index.html",
		"/_next/static/../../index.html",
		"/_next/static/..%2f..%2ffavicon.ico",
		"/_next/static//x",
		"/_next/static//chunks/main-abc.js",
		"/_next/static/chunks/",
		"/_next/static/chunks",
		"/_next/static/chunks/main-abc.js/",
		"/_next/static/chunks/./main-abc.js",
		"/_next/static/",
	} {
		rec := get(t, h, p)
		body := rec.Body.String()
		if rec.Code != 404 || rec.Header().Get("Location") != "" || strings.Contains(body, "<html") || strings.Contains(body, "console.log") ||
			rec.Header().Get("Content-Security-Policy") != "" {
			t.Errorf("%s: %d Location %q body %q", p, rec.Code, rec.Header().Get("Location"), body)
		}
	}
}

func TestETag(t *testing.T) {
	h, _ := New(export())
	etag := get(t, h, "/").Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}
	if rec := get(t, h, "/", "If-None-Match", etag); rec.Code != 304 || rec.Body.Len() != 0 {
		t.Fatalf("conditional GET: %d", rec.Code)
	}
}

func TestPlaceholderWithoutDashboard(t *testing.T) {
	h, _ := New(fstest.MapFS{".gitkeep": {}})
	rec := get(t, h, "/anything")
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "make dashboard") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	for _, p := range []string{"/metrics", "/app.js"} {
		if rec := get(t, h, p); rec.Code != 404 {
			t.Errorf("%s without a dashboard: %d, want 404", p, rec.Code)
		}
	}
}

func TestEmbeddedFSLoads(t *testing.T) {
	if _, err := New(Embedded()); err != nil {
		t.Fatal(err)
	}
}

func TestStyleHashesFromManifest(t *testing.T) {
	fsys := export()
	good := "'sha256-38RhXrc7EdReTKsOm23ZPOCUgniTUUcjky8QOOrQx6o='"
	fsys["csp.json"] = &fstest.MapFile{Data: []byte(`{"styleSrcElem":["` + good + `"]}`)}
	h, err := New(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if csp := get(t, h, "/").Header().Get("Content-Security-Policy"); !strings.Contains(csp, "style-src example.com/_next/static/ "+good+";") {
		t.Fatalf("style hash missing: %s", csp)
	}
	for _, bad := range []string{`'unsafe-inline'`, `* data:`, `'sha256-x'; script-src *`} {
		fsys["csp.json"] = &fstest.MapFile{Data: []byte(`{"styleSrcElem":["` + strings.ReplaceAll(bad, `"`, `\"`) + `"]}`)}
		if _, err := New(fsys); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}
