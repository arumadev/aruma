package console

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/arumadev/aruma/internal/services/storage/core"
)

func names(items any) []string {
	var out []string
	for _, it := range items.([]any) {
		out = append(out, it.(map[string]any)["name"].(string))
	}
	return out
}

func strs(v any) []string {
	var out []string
	for _, s := range v.([]any) {
		out = append(out, s.(string))
	}
	return out
}

// Browsing goes by folder: a prefix's subfolders and the objects directly in it, a page at a
// time, names with an empty segment included (the dashboard sees what GCS sees).
func TestBrowse(t *testing.T) {
	e := newEnv(t, true)
	for _, n := range []string{"a.txt", "b.txt", "dir/x.html", "dir/sub/y.txt", "dir/sub/z.txt", "odd//name", "folder/"} {
		e.insert(n, "", "data")
	}
	got := e.do(amalSession, "GET", path("/objects"), nil)
	if got.code != http.StatusOK {
		t.Fatalf("root: %d %s", got.code, got.body)
	}
	if p, i := strs(got.json["prefixes"]), names(got.json["items"]); !slices.Equal(p, []string{"dir/", "folder/", "odd/"}) ||
		!slices.Equal(i, []string{"a.txt", "b.txt"}) {
		t.Errorf("root: prefixes %v, items %v", p, i)
	}
	got = e.do(amalSession, "GET", path("/objects", "prefix", "dir/"), nil)
	if p, i := strs(got.json["prefixes"]), names(got.json["items"]); !slices.Equal(p, []string{"dir/sub/"}) || !slices.Equal(i, []string{"dir/x.html"}) {
		t.Errorf("dir/: prefixes %v, items %v", p, i)
	}
	got = e.do(amalSession, "GET", path("/objects", "prefix", "odd/"), nil)
	if p := strs(got.json["prefixes"]); !slices.Equal(p, []string{"odd//"}) {
		t.Errorf("odd/: prefixes %v", p)
	}
	got = e.do(amalSession, "GET", path("/objects", "prefix", "folder/"), nil)
	if i := names(got.json["items"]); !slices.Equal(i, []string{"folder/"}) {
		t.Errorf("folder/: items %v", i)
	}

	// Pages: the whole sub-folder two at a time.
	var seen []string
	token := ""
	for range 5 {
		q := []string{"prefix", "dir/sub/", "pageSize", "1"}
		if token != "" {
			q = append(q, "pageToken", token)
		}
		got := e.do(amalSession, "GET", path("/objects", q...), nil)
		if got.code != http.StatusOK {
			t.Fatalf("page: %d %s", got.code, got.body)
		}
		seen = append(seen, names(got.json["items"])...)
		token, _ = got.json["nextPageToken"].(string)
		if token == "" {
			break
		}
	}
	if !slices.Equal(seen, []string{"dir/sub/y.txt", "dir/sub/z.txt"}) {
		t.Errorf("paged: %v", seen)
	}
	for _, q := range [][]string{{"pageSize", "0"}, {"pageSize", "x"}, {"pageToken", "forged"}} {
		if got := e.do(amalSession, "GET", path("/objects", q...), nil); got.code != http.StatusBadRequest {
			t.Errorf("%v: %d %s, want 400", q, got.code, got.body)
		}
	}
}

// An object's metadata, its edit (audited), and its tokens: made and revoked from the
// dashboard, each a download link; the audit log never holds a token.
func TestObjectMetadataAndTokens(t *testing.T) {
	e := newEnv(t, true)
	e.insert("docs/a b.txt", "text/plain", "hello")
	name := "docs/a b.txt"
	got := e.do(amalSession, "GET", path("/object", "name", name), nil)
	if got.code != http.StatusOK || got.json["contentType"] != "text/plain" || got.json["size"] != float64(5) ||
		len(got.json["downloadTokens"].([]any)) != 0 || got.json["md5Hash"] != "XUFAKrxLKna5cZ2REBfFkg==" {
		t.Fatalf("get: %d %s", got.code, got.body)
	}
	if got := e.do(amalSession, "GET", path("/object"), nil); got.code != http.StatusBadRequest {
		t.Errorf("get without a name: %d", got.code)
	}
	if got := e.do(amalSession, "GET", path("/object", "name", "missing"), nil); got.code != http.StatusNotFound {
		t.Errorf("get a missing object: %d", got.code)
	}

	// The edit: a field set, one deleted, a custom key set.
	got = e.do(amalSession, "PATCH", path("/object", "name", name), map[string]any{
		"contentType": "text/markdown", "cacheControl": "no-cache", "metadata": map[string]any{"k": "v", "gone": nil}})
	if got.code != http.StatusOK || got.json["contentType"] != "text/markdown" || got.json["metageneration"] != "2" ||
		got.json["metadata"].(map[string]any)["k"] != "v" {
		t.Fatalf("patch: %d %s", got.code, got.body)
	}
	got = e.do(amalSession, "PATCH", path("/object", "name", name), map[string]any{"cacheControl": nil, "metadata": nil})
	if got.code != http.StatusOK || got.json["cacheControl"] != nil || len(got.json["metadata"].(map[string]any)) != 0 {
		t.Errorf("patch to delete: %d %s", got.code, got.body)
	}
	if a, ok := e.lastAudit("storage.object.update"); !ok || a.actor != amal || a.target != name ||
		!slices.Equal(strs(a.details["fields"]), []string{"cacheControl", "metadata"}) {
		t.Errorf("update audit: %+v", a)
	}
	for _, body := range []any{map[string]any{"size": 3}, map[string]any{"contentType": 3}, map[string]any{"metadata": "x"},
		map[string]any{"metadata": map[string]any{"k": 1}}, "[]"} {
		if got := e.do(amalSession, "PATCH", path("/object", "name", name), body); got.code != http.StatusBadRequest {
			t.Errorf("patch %v: %d %s, want 400", body, got.code, got.body)
		}
	}

	// Tokens.
	got = e.do(amalSession, "POST", path("/object:createToken", "name", name), nil)
	tokens := strs(got.json["downloadTokens"])
	if got.code != http.StatusOK || len(tokens) != 1 {
		t.Fatalf("createToken: %d %s", got.code, got.body)
	}
	wantURL := publicURL + "/v0/b/chat-app.aruma.test/o/docs%2Fa%20b.txt?alt=media&token=" + tokens[0]
	if urls := strs(got.json["downloadUrls"]); !slices.Equal(urls, []string{wantURL}) {
		t.Errorf("downloadUrls %v, want %s", urls, wantURL)
	}
	got = e.do(amalSession, "POST", path("/object:createToken", "name", name), nil)
	if len(strs(got.json["downloadTokens"])) != 2 {
		t.Fatalf("second createToken: %s", got.body)
	}
	if got := e.do(amalSession, "POST", path("/object:deleteToken", "name", name), map[string]any{}); got.code != http.StatusBadRequest {
		t.Errorf("deleteToken without a token: %d", got.code)
	}
	got = e.do(amalSession, "POST", path("/object:deleteToken", "name", name), map[string]any{"token": tokens[0]})
	if left := strs(got.json["downloadTokens"]); got.code != http.StatusOK || len(left) != 1 || left[0] == tokens[0] {
		t.Errorf("deleteToken: %d %s", got.code, got.body)
	}
	for _, action := range []string{"storage.object.token.create", "storage.object.token.delete"} {
		if a, ok := e.lastAudit(action); !ok || a.target != name {
			t.Errorf("%s audit: %+v", action, a)
		}
	}
	for _, a := range e.audits() {
		for _, v := range a.details {
			if s, _ := v.(string); s == tokens[0] {
				t.Errorf("the audit log holds a token: %+v", a)
			}
		}
	}

	// Deletion.
	if got := e.do(amalSession, "DELETE", path("/object", "name", name), nil); got.code != http.StatusOK {
		t.Fatalf("delete: %d %s", got.code, got.body)
	}
	if got := e.do(amalSession, "GET", path("/object", "name", name), nil); got.code != http.StatusNotFound {
		t.Errorf("get after delete: %d", got.code)
	}
	if got := e.do(amalSession, "DELETE", path("/object", "name", name), nil); got.code != http.StatusNotFound {
		t.Errorf("delete again: %d", got.code)
	}
	if a, ok := e.lastAudit("storage.object.delete"); !ok || a.actor != amal || a.target != name {
		t.Errorf("delete audit: %+v", a)
	}
}

// A write whose audit line cannot be written says so: the change stands, and the dashboard is
// told the log missed it.
func TestWriteWithoutAuditFails(t *testing.T) {
	e := newEnv(t, true)
	e.insert("a.txt", "", "x")
	e.failAudit = true
	got := e.do(amalSession, "DELETE", path("/object", "name", "a.txt"), nil)
	if got.code != http.StatusInternalServerError || !strings.Contains(string(got.body), "audit log") {
		t.Errorf("delete without the audit log: %d %s", got.code, got.body)
	}
}

// The dashboard's download is always attachment, with every header media writes: the stored
// type, nosniff, the sandbox CSP, no-store, and the general same-origin CORP (no cross-origin
// override); scripts and workers loaded from the dashboard's origin are refused.
func TestDownloadIsAlwaysAttachment(t *testing.T) {
	e := newEnv(t, true)
	o := e.insert("site/index.html", "text/html", "<script>alert(1)</script>")
	_, err := e.svc.Update(t.Context(), e.bucket, o.Name, core.Principal{Kind: core.PrincipalAdmin, ServiceAccount: "sa"},
		core.SurfaceGCS, core.Patch{Fields: map[string]*string{core.FieldContentDisposition: new(`inline; filename="index.html"`)}},
		core.Preconditions{})
	if err != nil {
		t.Fatal(err)
	}
	got := e.do(amalSession, "GET", path("/object:download", "name", "site/index.html"), nil)
	if got.code != http.StatusOK || string(got.body) != "<script>alert(1)</script>" {
		t.Fatalf("download: %d %s", got.code, got.body)
	}
	for h, want := range map[string]string{
		"Content-Type":                 "text/html",
		"Content-Disposition":          `attachment; filename="index.html"`,
		"X-Content-Type-Options":       "nosniff",
		"Cross-Origin-Resource-Policy": "",
		"Content-Length":               "25",
	} {
		if v := got.header.Get(h); v != want {
			t.Errorf("%s: %q, want %q", h, v, want)
		}
	}
	if csp := got.header.Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox") {
		t.Errorf("CSP %q: want sandbox", csp)
	}
	if cc := got.header.Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control %q: want no-store", cc)
	}

	// An object without a stored disposition gets attachment with its last segment.
	e.insert("a/b/report.txt", "text/plain", "r")
	if got := e.do(amalCLI, "GET", path("/object:download", "name", "a/b/report.txt"), nil); got.header.Get("Content-Disposition") != `attachment; filename*=utf-8''report.txt` {
		t.Errorf("default disposition: %q", got.header.Get("Content-Disposition"))
	}

	// Loaded as a script from the dashboard's own page.
	req := httptest.NewRequest("GET", path("/object:download", "name", "site/index.html"), nil)
	e.authorize(req, amalSession)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Sec-Fetch-Dest", "script")
	if got := e.serve(req); got.code != http.StatusForbidden {
		t.Errorf("as a same-origin script: %d", got.code)
	}

	// A range, and a missing object.
	req = httptest.NewRequest("GET", path("/object:download", "name", "site/index.html"), nil)
	e.authorize(req, amalSession)
	req.Header.Set("Range", "bytes=0-7")
	if got := e.serve(req); got.code != http.StatusPartialContent || string(got.body) != "<script>" {
		t.Errorf("range: %d %q", got.code, got.body)
	}
	if got := e.do(amalSession, "GET", path("/object:download", "name", "missing"), nil); got.code != http.StatusNotFound {
		t.Errorf("download a missing object: %d", got.code)
	}
}
