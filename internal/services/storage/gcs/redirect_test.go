package gcs

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/arumadev/aruma/internal/services/storage/core"
)

// The JSON and media routes never answer a redirect. @google-cloud/storage sends their requests
// through teeny-request, which follows a redirect with its Authorization header kept when the
// host is the same (sdk/node/admin-storage/README.md), so a redirect from here to another port
// of the host would hand that port the service account's Bearer. Only the resumable session
// answers a 308, without a Location (wire.WriteIncomplete), and only under /upload/storage/v1.

// redirect is a status node-fetch follows. 304 Not Modified is not one.
func redirect(code int) bool {
	switch code {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	}
	return false
}

func TestJSONAndMediaNeverRedirect(t *testing.T) {
	e := newEnv(t)
	o := e.put(e.a, "dir/a.txt", "hello aruma\n", core.Patch{})
	e.put(e.a, "src.txt", "copy me", core.Patch{})
	gen := strconv.FormatInt(o.Generation, 10)
	const bucket = "/storage/v1/b/" + bucketA
	obj := mediaPath + "dir%2Fa.txt"
	etag := e.do("GET", obj+"?alt=media", tokenA, nil).Header().Get("ETag")
	for _, c := range []struct {
		method, target, bearer, body string
		header                       []string
	}{
		{"GET", bucket, tokenA, "", nil},
		{"GET", bucket, "", "", nil},
		{"GET", bucket, tokenB, "", nil},
		{"PATCH", bucket, tokenA, `{"cors":[]}`, []string{"Content-Type", "application/json"}},
		{"GET", "/storage/v1/b/nobody-app.aruma.test", tokenA, "", nil},
		{"GET", bucket + "/o?prefix=dir/&delimiter=/", tokenA, "", nil},
		{"GET", bucket + "/o?pageToken=garbage", tokenA, "", nil},
		{"GET", obj, tokenA, "", nil},
		{"GET", obj + "?generation=" + gen, tokenA, "", nil},
		{"GET", obj + "?generation=1", tokenA, "", nil},
		{"GET", obj + "?ifGenerationMatch=1", tokenA, "", nil},
		{"GET", mediaPath + "missing", tokenA, "", nil},
		{"GET", mediaPath + "dir/a.txt", tokenA, "", nil}, // an unescaped slash
		{"GET", obj + "/", tokenA, "", nil},
		{"GET", obj + "?alt=media", tokenA, "", nil},
		{"GET", obj + "?alt=media", "", "", nil},
		{"GET", obj + "?alt=media", tokenB, "", nil},
		{"GET", obj + "?alt=media", tokenA, "", []string{"Range", "bytes=2-5"}},
		{"GET", obj + "?alt=media", tokenA, "", []string{"Range", "bytes=900-"}},
		{"GET", obj + "?alt=media", tokenA, "", []string{"If-None-Match", etag}},
		{"GET", obj + "?alt=media&generation=1", tokenA, "", nil},
		{"HEAD", obj + "?alt=media", tokenA, "", nil},
		{"GET", "/download" + obj + "?alt=media", tokenA, "", nil},
		{"GET", "/download" + obj + "?generation=" + gen + "&alt=media", tokenA, "", nil},
		{"GET", "/download" + obj, tokenA, "", nil},
		{"GET", "/download" + mediaPath + "missing?alt=media", tokenA, "", nil},
		{"PATCH", obj, tokenA, `{"metadata":{"k":"v"}}`, []string{"Content-Type", "application/json"}},
		{"PATCH", obj, tokenA, `not json`, []string{"Content-Type", "application/json"}},
		{"POST", mediaPath + "src.txt/rewriteTo/b/" + bucketA + "/o/copy.txt", tokenA, "{}", []string{"Content-Type", "application/json"}},
		{"POST", mediaPath + "src.txt/rewriteTo/b/" + bucketB + "/o/copy.txt", tokenA, "{}", []string{"Content-Type", "application/json"}},
		{"DELETE", mediaPath + "copy.txt", tokenA, "", nil},
		{"DELETE", mediaPath + "copy.txt", tokenA, "", nil},
		{"PUT", obj, tokenA, "{}", nil},
		{"OPTIONS", obj, "", "", []string{"Origin", "https://app.example", "Access-Control-Request-Method", "GET"}},
	} {
		var body io.Reader
		if c.body != "" {
			body = strings.NewReader(c.body)
		}
		rec := e.do(c.method, c.target, c.bearer, body, c.header...)
		if redirect(rec.Code) || rec.Header().Get("Location") != "" {
			t.Errorf("%s %s (%v): %d with Location %q", c.method, c.target, c.header, rec.Code, rec.Header().Get("Location"))
		}
	}
}

// The same, from the source, for routes the table above does not know yet: in this package and
// in media, no redirect status and no http.Redirect; the one Location and the one 308 writer
// belong to the upload session.
func TestOnlyTheUploadSessionWritesLocationOr308(t *testing.T) {
	statuses := regexp.MustCompile(`http\.Redirect\b|StatusMovedPermanently|StatusFound|StatusSeeOther|StatusTemporaryRedirect|StatusPermanentRedirect|\b30[12378]\b`)
	location := regexp.MustCompile(`"Location"`)
	incomplete := regexp.MustCompile(`WriteIncomplete`)
	for _, dir := range []string{".", "../media"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil || len(files) == 0 {
			t.Fatalf("no sources in %s: %v", dir, err)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			for i, line := range strings.Split(string(b), "\n") {
				code, _, _ := strings.Cut(line, "//")
				where := f + ":" + strconv.Itoa(i+1)
				if statuses.MatchString(code) {
					t.Errorf("%s: a redirect status outside the upload session: %s", where, strings.TrimSpace(line))
				}
				if (location.MatchString(code) || incomplete.MatchString(code)) && filepath.Base(f) != "upload.go" {
					t.Errorf("%s: Location or a 308 outside upload.go: %s", where, strings.TrimSpace(line))
				}
			}
		}
	}
}
