package acceptance

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	dto "github.com/prometheus/client_model/go"

	storage "github.com/arumadev/aruma/internal/services/storage/core"
)

// S3-C's acceptance cases: alt=media on /v0 (A §3.3, ADR-022 §9, §10, §11), on a real server
// and Postgres. The objects are put through core.Insert on the /v0 surface as an Admin, so each
// has its download token and the stored inline disposition, as an SDK upload leaves it. The
// browsers' side of the same headers is test/e2e/browser-matrix.

// mediaReply is one download's reply: the bytes as they came, never decoded.
type mediaReply struct {
	status int
	header http.Header
	body   []byte
	err    error // reading the body failed: the server cut the reply
}

// media sends one request for an object's bytes; auth is the Authorization header ("" for
// none). Go's client neither asks for gzip nor decodes it: what is asked for is in header.
func (e *v0Env) media(method, path, auth string, header map[string]string) mediaReply {
	e.t.Helper()
	req, err := http.NewRequest(method, e.URL+path, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	req.Header.Set("Accept-Encoding", "identity")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return mediaReply{status: resp.StatusCode, header: resp.Header, body: b, err: err}
}

// putV0 stores an object as a /v0 upload does (a download token, the default disposition), with
// the content fields given.
func (e *v0Env) putV0(name, body string, fields map[string]string) storage.Object {
	e.t.Helper()
	spec := storage.UploadSpec{Name: name, Declared: int64(len(body)), Surface: storage.SurfaceFirebase,
		Attrs: storage.Patch{Fields: map[string]*string{}}}
	for k, v := range fields {
		spec.Attrs.Fields[k] = new(v)
	}
	o, err := e.App.Storage.Insert(context.Background(), e.bucket, storage.Principal{Kind: storage.PrincipalAdmin}, spec,
		strings.NewReader(body), int64(len(body)))
	if err != nil {
		e.t.Fatal(err)
	}
	if len(o.DownloadTokens) != 1 {
		e.t.Fatalf("%s: tokens %q", name, o.DownloadTokens)
	}
	return o
}

func (e *v0Env) expectMedia(r mediaReply, status int, body, what string) {
	e.t.Helper()
	if r.status != status {
		e.t.Fatalf("%s: %d, want %d: %s", what, r.status, status, r.body)
	}
	if status < 300 && string(r.body) != body {
		e.t.Fatalf("%s: body %q, want %q", what, r.body, body)
	}
	if status >= 400 {
		if ct := r.header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") || !bytes.Contains(r.body, []byte(`"code":`)) {
			e.t.Fatalf("%s: not /v0's error shape: %s %s", what, ct, r.body)
		}
	}
}

// Who opens the bytes (ADR-022 §8, §9): a matching token alone, whatever the rules and whoever
// asks; a token that matches none 403 without the rules, on an object or on nothing; without
// a token the rules, 403 before 404; the Admin past the rules.
func TestStorageV0DownloadAccess(t *testing.T) {
	e := newV0Env(t)
	priv := e.putV0("users/alice/p.txt", "private bytes", map[string]string{storage.FieldContentType: "text/plain"})
	pub := e.putV0("public/p.txt", "public bytes", nil)
	pt, ut := "?alt=media&token="+priv.DownloadTokens[0], "?alt=media&token="+pub.DownloadTokens[0]
	path, pubPath := e.obj(priv.Name), e.obj(pub.Name)

	e.expectMedia(e.media("GET", path+pt, "", nil), 200, "private bytes", "the token, anonymous")
	e.expectMedia(e.media("GET", path+pt, firebase(e.bob), nil), 200, "private bytes", "the token, as bob")
	e.expectMedia(e.media("GET", path+"?alt=media", firebase(e.alice), nil), 200, "private bytes", "alice by the rules")
	e.expectMedia(e.media("GET", path+"?alt=media", bearer(e.sa), nil), 200, "private bytes", "the service account")
	e.expectMedia(e.media("GET", path+"?alt=media", bearer(e.pat), nil), 200, "private bytes", "the owner's CLI token")
	e.expectMedia(e.media("GET", path+"?alt=media", "", nil), 403, "", "anonymous by the rules")
	e.expectMedia(e.media("GET", path+"?alt=media", firebase(e.bob), nil), 403, "", "bob by the rules")
	// An empty token is none: the rules decide.
	e.expectMedia(e.media("GET", path+"?alt=media&token=", "", nil), 403, "", "an empty token, anonymous")
	e.expectMedia(e.media("GET", path+"?alt=media&token=", firebase(e.alice), nil), 200, "private bytes", "an empty token, alice")

	// A wrong token is 403 even where the rules would allow, and for the owner of the folder.
	e.expectMedia(e.media("GET", pubPath+"?alt=media&token=nope", "", nil), 403, "", "a wrong token on a public file")
	e.expectMedia(e.media("GET", path+"?alt=media&token=nope", firebase(e.alice), nil), 403, "", "a wrong token, alice")
	e.expectMedia(e.media("GET", pubPath+pt, "", nil), 403, "", "another object's token")
	e.expectMedia(e.media("GET", pubPath+ut, "", nil), 200, "public bytes", "its own token")
	e.expectMedia(e.media("GET", e.obj("public/none")+ut, "", nil), 403, "", "a token on nothing")

	// Without a token: 404 only where the rules allow (D §7.1).
	e.expectMedia(e.media("GET", e.obj("public/none")+"?alt=media", "", nil), 404, "", "nothing, allowed")
	e.expectMedia(e.media("GET", e.obj("users/alice/none")+"?alt=media", "", nil), 403, "", "nothing, denied")
	e.expectMedia(e.media("GET", e.obj("users/alice/none")+"?alt=media", firebase(e.alice), nil), 404, "", "nothing, alice")

	// A credential that does not verify is 401 before the token is looked at (the router).
	e.expectMedia(e.media("GET", path+pt, firebase("garbage"), nil), 401, "", "a bad ID token with a token")

	// The token opens the bytes, never the metadata (ADR-023 §3-18).
	e.expect(e.call("GET", path+"?token="+priv.DownloadTokens[0], "", "", nil), 403, "the metadata by token")

	// A token removed by the admin stops opening the bytes (Pyric revokedUrlStatus: 403).
	if _, err := e.App.Storage.DeleteToken(context.Background(), e.bucket, priv.Name,
		storage.Principal{Kind: storage.PrincipalAdmin}, priv.DownloadTokens[0]); err != nil {
		t.Fatal(err)
	}
	e.expectMedia(e.media("GET", path+pt, "", nil), 403, "", "a revoked token")
}

// The reply's headers (ADR-022 §10): media's, through the router. Cache-Control by mode: the
// stored value on a token link, private on the rules' decision, anonymous or not.
func TestStorageV0DownloadHeaders(t *testing.T) {
	e := newV0Env(t)
	o := e.putV0("public/a b.html", "<script>x</script>", map[string]string{
		storage.FieldContentType: "text/html", storage.FieldCacheControl: "public, max-age=60, s-maxage=600",
		storage.FieldContentLanguage: "ar",
	})
	path := e.obj(o.Name)
	tok := "?alt=media&token=" + o.DownloadTokens[0]

	r := e.media("GET", path+tok, "", nil)
	e.expectMedia(r, 200, "<script>x</script>", "by token")
	for k, want := range map[string]string{
		"Content-Type":                   "text/html",
		"X-Content-Type-Options":         "nosniff",
		"Content-Security-Policy":        "default-src 'none'; style-src 'unsafe-inline'; sandbox",
		"Cross-Origin-Resource-Policy":   "cross-origin",
		"X-Frame-Options":                "DENY",
		"Content-Disposition":            "inline; filename*=utf-8''a%20b.html",
		"Cache-Control":                  "public, max-age=60, s-maxage=600",
		"Content-Language":               "ar",
		"Accept-Ranges":                  "bytes",
		"Content-Length":                 "18",
		"X-Goog-Generation":              strconv.FormatInt(o.Generation, 10),
		"X-Goog-Stored-Content-Encoding": "identity",
		"Access-Control-Allow-Origin":    "",
	} {
		if got := r.header.Get(k); got != want {
			t.Errorf("%s: %q, want %q", k, got, want)
		}
	}
	if h := r.header.Get("X-Goog-Hash"); !strings.HasPrefix(h, "crc32c=") || !strings.Contains(h, ",md5=") {
		t.Errorf("X-Goog-Hash %q", h)
	}
	etag := r.header.Get("ETag")
	if !strings.HasPrefix(etag, `"`) || r.header.Get("Last-Modified") == "" {
		t.Errorf("ETag %q, Last-Modified %q", etag, r.header.Get("Last-Modified"))
	}

	for _, auth := range []string{"", firebase(e.alice), bearer(e.sa)} {
		r := e.media("GET", path+"?alt=media", auth, nil)
		e.expectMedia(r, 200, "<script>x</script>", "by the rules")
		if got := r.header.Get("Cache-Control"); got != "private, max-age=60" {
			t.Errorf("rules, auth %.10q: Cache-Control %q", auth, got)
		}
	}

	// HEAD: the headers alone.
	r = e.media("HEAD", path+tok, "", nil)
	if r.status != 200 || len(r.body) != 0 || r.header.Get("Content-Length") != "18" {
		t.Errorf("HEAD: %d %q %v", r.status, r.body, r.header)
	}
	// A cached copy revalidates on the media ETag.
	r = e.media("GET", path+tok, "", map[string]string{"If-None-Match": etag})
	if r.status != 304 {
		t.Errorf("If-None-Match: %d", r.status)
	}
	// A metadata change keeps the media ETag, so a download in progress goes on (ADR-022 §12).
	e.expect(e.call("PATCH", path, firebase(e.alice), `{"contentLanguage":"en"}`, nil), 200, "PATCH")
	r = e.media("GET", path+tok, "", nil)
	if r.header.Get("ETag") != etag || r.header.Get("Content-Language") != "en" {
		t.Errorf("after PATCH: ETag %q (was %q), Content-Language %q", r.header.Get("ETag"), etag, r.header.Get("Content-Language"))
	}

	// No stored type: never a sniffed one, even for x.html.
	x := e.putV0("public/x.html", "<html><script>alert(1)</script></html>", nil)
	r = e.media("GET", e.obj(x.Name)+"?alt=media", "", nil)
	if ct := r.header.Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("x.html without a type: %q", ct)
	}
	// A PDF is attachment whatever is stored (§10.2).
	pdf := e.putV0("public/report.pdf", "%PDF-1.4", map[string]string{storage.FieldContentType: "application/pdf"})
	r = e.media("GET", e.obj(pdf.Name)+"?alt=media", "", nil)
	if cd := r.header.Get("Content-Disposition"); cd != "attachment; filename*=utf-8''report.pdf" {
		t.Errorf("PDF: Content-Disposition %q", cd)
	}
}

// Range (ADR-022 §10.3) and the refusals of media (§10.1), through the router.
func TestStorageV0DownloadRange(t *testing.T) {
	e := newV0Env(t)
	o := e.putV0("public/r.bin", "0123456789", nil)
	path := e.obj(o.Name) + "?alt=media"
	cases := []struct {
		rng, contentRange string
		status            int
		body              string
	}{
		{"bytes=0-0", "bytes 0-0/10", 206, "0"},
		{"bytes=7-", "bytes 7-9/10", 206, "789"},
		{"bytes=-2", "bytes 8-9/10", 206, "89"},
		{"bytes=3-100", "bytes 3-9/10", 206, "3456789"},
		{"bytes=10-", "bytes */10", 416, ""},
		{"bytes=0-5,3-9", "", 200, "0123456789"},
	}
	for _, c := range cases {
		r := e.media("GET", path, "", map[string]string{"Range": c.rng})
		if r.status != c.status || r.header.Get("Content-Range") != c.contentRange || (c.status < 300 && string(r.body) != c.body) {
			t.Errorf("Range %s: %d %q %q", c.rng, r.status, r.header.Get("Content-Range"), r.body)
		}
	}
	// JS asks with a Range even for an empty object (maxDownloadSizeBytes): 200, never 416.
	empty := e.putV0("public/empty", "", nil)
	r := e.media("GET", e.obj(empty.Name)+"?alt=media", "", map[string]string{"Range": "bytes=0-1000"})
	if r.status != 200 || len(r.body) != 0 {
		t.Errorf("empty object with a Range: %d %q", r.status, r.body)
	}

	// A service worker's script, and a script from the dashboard's own origin, are refused.
	for _, h := range []map[string]string{
		{"Service-Worker": "script"},
		{"Sec-Fetch-Dest": "serviceworker", "Sec-Fetch-Site": "same-origin"},
		{"Sec-Fetch-Dest": "script", "Sec-Fetch-Site": "same-origin"},
	} {
		e.expectMedia(e.media("GET", path, "", h), 403, "", "refused: "+strings.Join(slices.Sorted(maps.Keys(h)), ","))
	}
	e.expectMedia(e.media("GET", path, "", map[string]string{"Sec-Fetch-Dest": "script", "Sec-Fetch-Site": "cross-site"}),
		200, "0123456789", "a script from an app's origin")
	e.expectMedia(e.media("GET", path, "", map[string]string{"Sec-Fetch-Dest": "image", "Sec-Fetch-Site": "same-origin"}),
		200, "0123456789", "an image from the dashboard")
}

// The bucket's CORS on alt=media (ADR-022 §11): none until the owner sets it, then the rule's
// origin, also on a refusal so the page reads the status; the API's open CORS is never there.
func TestStorageV0DownloadCORS(t *testing.T) {
	e := newV0Env(t)
	o := e.putV0("public/c.txt", "cors", nil)
	path := e.obj(o.Name) + "?alt=media"
	app := map[string]string{"Origin": "https://app.example"}

	r := e.media("GET", path, "", app)
	e.expectMedia(r, 200, "cors", "no CORS set")
	if r.header.Get("Access-Control-Allow-Origin") != "" || r.header.Get("Vary") != "Origin" {
		t.Errorf("no CORS set: %v", r.header)
	}

	if _, err := e.App.Storage.SetCORS(context.Background(), e.bucket, storage.Principal{Kind: storage.PrincipalAdmin},
		[]storage.CORSRule{{Origins: []string{"https://app.example"}, Methods: []string{"GET"},
			ResponseHeaders: []string{"Content-Range", "Range"}, MaxAgeSeconds: 600}}); err != nil {
		t.Fatal(err)
	}
	r = e.media("GET", path, "", app)
	e.expectMedia(r, 200, "cors", "CORS set")
	if r.header.Get("Access-Control-Allow-Origin") != "https://app.example" ||
		r.header.Get("Access-Control-Expose-Headers") != "Content-Range, Range" {
		t.Errorf("CORS set: %v", r.header)
	}
	r = e.media("GET", path, "", map[string]string{"Origin": "https://other.example"})
	if r.header.Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("another origin: %v", r.header)
	}
	r = e.media("GET", e.obj("users/alice/none")+"?alt=media", "", app)
	e.expectMedia(r, 403, "", "a refusal")
	if r.header.Get("Access-Control-Allow-Origin") != "https://app.example" {
		t.Errorf("a refusal: %v", r.header)
	}
	// The preflight of a download with a Range of two ranges, which is not safelisted.
	r = e.media("OPTIONS", path, "", map[string]string{"Origin": "https://app.example",
		"Access-Control-Request-Method": "GET", "Access-Control-Request-Headers": "range"})
	if r.status != 204 || r.header.Get("Access-Control-Allow-Origin") != "https://app.example" ||
		r.header.Get("Access-Control-Allow-Methods") != "GET" || r.header.Get("Access-Control-Max-Age") != "600" {
		t.Errorf("preflight: %d %v", r.status, r.header)
	}
}

// Decompressive transcoding through the router (ADR-022 §10.7): the stored gzip bytes to a
// client that takes them, the decompressed bytes to one that does not, and a reply cut at
// object_bytes, counted.
func TestStorageV0DownloadGzip(t *testing.T) {
	e := newV0Env(t)
	plain := strings.Repeat("hello gzip ", 100)
	o := e.putV0("public/g.txt", gzipped(t, plain), map[string]string{
		storage.FieldContentType: "text/plain", storage.FieldContentEncoding: "gzip"})
	path := e.obj(o.Name) + "?alt=media"

	r := e.media("GET", path, "", map[string]string{"Accept-Encoding": "gzip"})
	e.expectMedia(r, 200, gzipped(t, plain), "with Accept-Encoding: gzip")
	if r.header.Get("Content-Encoding") != "gzip" {
		t.Errorf("stored bytes: %v", r.header)
	}
	r = e.media("GET", path, "", map[string]string{"Range": "bytes=0-3"})
	e.expectMedia(r, 200, plain, "decompressed")
	// No Content-Length from media (net/http adds one to a reply this small).
	if r.header.Get("Content-Encoding") != "" || r.header.Get("X-Goog-Stored-Content-Encoding") != "gzip" {
		t.Errorf("decompressed: %v", r.header)
	}
}

// A gzip bomb against a small object_bytes: the reply is cut once the decompressed bytes pass it,
// and DecompressionsCut counts the cut.
func TestStorageV0DownloadGzipCut(t *testing.T) {
	e := &v0Env{Env: newEnv(t, map[string]string{"ARUMA_STORAGE_ENABLED": "true", "ARUMA_STORAGE_DATA_DIR": t.TempDir(),
		"ARUMA_STORAGE_OBJECT_BYTES": "65536"})}
	b, err := e.App.Storage.Bucket(context.Background(), e.owner().project("chat-app").str("storageBucket"))
	if err != nil {
		t.Fatal(err)
	}
	e.bucket = b
	bomb := gzipped(t, strings.Repeat("\x00", 1<<20)) // 1 MiB from about 1 KiB
	o := e.putV0("bomb.txt", bomb, map[string]string{storage.FieldContentEncoding: "gzip"})
	before := counter(t, e.App.Storage.Metrics().DecompressionsCut)
	r := e.media("GET", e.obj(o.Name)+"?alt=media&token="+o.DownloadTokens[0], "", nil)
	if r.status != 200 || r.err == nil || int64(len(r.body)) > 65536 {
		t.Fatalf("the bomb: %d, %d bytes, err %v", r.status, len(r.body), r.err)
	}
	if got := counter(t, e.App.Storage.Metrics().DecompressionsCut); got != before+1 {
		t.Errorf("DecompressionsCut %v, want %v", got, before+1)
	}
}

// A gzip object under the bucket's CORS varies on both: Origin and Accept-Encoding, each once,
// on the stored bytes, the decompressed ones and a 304, so a cache never gives one origin's reply
// to another.
func TestStorageV0DownloadVary(t *testing.T) {
	e := newV0Env(t)
	if _, err := e.App.Storage.SetCORS(context.Background(), e.bucket, storage.Principal{Kind: storage.PrincipalAdmin},
		[]storage.CORSRule{{Origins: []string{"*"}, Methods: []string{"GET"}, MaxAgeSeconds: 3600}}); err != nil {
		t.Fatal(err)
	}
	o := e.putV0("public/v.txt", gzipped(t, strings.Repeat("vary ", 50)), map[string]string{
		storage.FieldContentType: "text/plain", storage.FieldContentEncoding: "gzip"})
	path := e.obj(o.Name) + "?alt=media&token=" + o.DownloadTokens[0]
	etag := e.media("GET", path, "", nil).header.Get("ETag")
	for _, c := range []struct {
		name   string
		header map[string]string
		status int
	}{
		{"gzip", map[string]string{"Accept-Encoding": "gzip"}, 200},
		{"identity", map[string]string{"Accept-Encoding": "identity"}, 200},
		{"304", map[string]string{"Accept-Encoding": "gzip", "If-None-Match": etag}, 304},
	} {
		c.header["Origin"] = "https://app.example"
		r := e.media("GET", path, "", c.header)
		if r.status != c.status || r.header.Get("Access-Control-Allow-Origin") != "https://app.example" {
			t.Fatalf("%s: %d %v", c.name, r.status, r.header)
		}
		var names []string
		for _, v := range r.header.Values("Vary") {
			for n := range strings.SplitSeq(v, ",") {
				names = append(names, strings.ToLower(strings.TrimSpace(n)))
			}
		}
		slices.Sort(names)
		if !slices.Equal(names, []string{"accept-encoding", "origin"}) {
			t.Errorf("%s: Vary %q", c.name, r.header.Values("Vary"))
		}
	}
}

// An object whose file is gone is the server's fault: 503, and no Retry-After, since it is not
// passing pressure and iOS would retry a Retry-After forever (ADR-023 §3-70).
func TestStorageV0DownloadUnavailable(t *testing.T) {
	dir := t.TempDir()
	e := &v0Env{Env: newEnv(t, map[string]string{"ARUMA_STORAGE_ENABLED": "true", "ARUMA_STORAGE_DATA_DIR": dir})}
	b, err := e.App.Storage.Bucket(context.Background(), e.owner().project("chat-app").str("storageBucket"))
	if err != nil {
		t.Fatal(err)
	}
	e.bucket = b
	const body = "the only file with these bytes"
	o := e.putV0("lost.txt", body, nil)
	removed := 0
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		if got, err := os.ReadFile(path); err == nil && string(got) == body {
			removed++
			return os.Remove(path)
		}
		return nil
	})
	if err != nil || removed != 1 {
		t.Fatalf("removing the object's file: %v, %d removed", err, removed)
	}
	r := e.media("GET", e.obj(o.Name)+"?alt=media&token="+o.DownloadTokens[0], "", nil)
	e.expectMedia(r, 503, "", "a lost file")
	if ra := r.header.Get("Retry-After"); ra != "" {
		t.Errorf("Retry-After %q on a 503 that is no pressure", ra)
	}
}

func gzipped(t *testing.T, s string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func counter(t *testing.T, c interface{ Write(*dto.Metric) error }) float64 {
	t.Helper()
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		t.Fatal(err)
	}
	return m.GetCounter().GetValue()
}
