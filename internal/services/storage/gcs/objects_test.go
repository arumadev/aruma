package gcs

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/arumadev/aruma/internal/services/storage/core"
	"github.com/arumadev/aruma/internal/services/storage/wire"
)

func decodeObject(t *testing.T, body []byte) wire.GCSObject {
	t.Helper()
	var o wire.GCSObject
	if err := json.Unmarshal(body, &o); err != nil {
		t.Fatalf("object %s: %v", body, err)
	}
	return o
}

const objA = "/storage/v1/b/" + bucketA + "/o/"

// objects.get as Go and Node send it: the resource, numbers as strings, the tokens in
// metadata; a missing object is 404 notFound in GCS's words, which exists() reads as false.
func TestGetObject(t *testing.T) {
	e := newEnv(t)
	put := e.put(e.a, "dir/a b.txt", "hello", core.Patch{
		Fields:   map[string]*string{core.FieldContentType: new("text/plain"), core.FieldCacheControl: new("no-store")},
		Metadata: map[string]*string{"k": new("v")},
		Tokens:   &[]string{"tok-1"},
	})
	for _, target := range []string{
		objA + "dir%2Fa%20b.txt?alt=json&prettyPrint=false&projection=full", // Go
		objA + "dir%2Fa%20b.txt?", // Node
		objA + "dir%2Fa%20b.txt?generation=" + strconv.FormatInt(put.Generation, 10) + "&userProject=x&fields=name",
		"/download" + objA + "dir%2Fa%20b.txt",
	} {
		rec := e.do("GET", target, tokenA, nil)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", target, rec.Code, rec.Body)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-cache, no-store, max-age=0, must-revalidate" {
			t.Errorf("Cache-Control %q", cc)
		}
		var raw map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &raw)
		for _, k := range []string{"generation", "metageneration", "size"} {
			if _, ok := raw[k].(string); !ok {
				t.Errorf("%s is %T, want a string", k, raw[k])
			}
		}
		o := decodeObject(t, rec.Body.Bytes())
		gen := strconv.FormatInt(put.Generation, 10)
		want := wire.GCSObject{Kind: "storage#object", ID: bucketA + "/dir/a b.txt/" + gen,
			SelfLink:  baseURL + "/storage/v1/b/" + bucketA + "/o/dir%2Fa%20b.txt",
			MediaLink: baseURL + "/download/storage/v1/b/" + bucketA + "/o/dir%2Fa%20b.txt?generation=" + gen + "&alt=media",
			Name:      "dir/a b.txt", Bucket: bucketA, Generation: gen, Metageneration: "1", ContentType: "text/plain",
			StorageClass: "STANDARD", Size: "5", MD5Hash: "XUFAKrxLKna5cZ2REBfFkg==", CRC32C: "mnG7TA==",
			ETag: wire.ETag(put.Generation, 1), CacheControl: "no-store", TimeCreated: wire.Time(put.Created),
			Updated: wire.Time(put.Updated), TimeStorageClassUpdated: wire.Time(put.Created), TimeFinalized: wire.Time(put.Created),
			Metadata: map[string]string{"k": "v", "firebaseStorageDownloadTokens": "tok-1"}}
		got, _ := json.Marshal(o)
		exp, _ := json.Marshal(want)
		if string(got) != string(exp) {
			t.Errorf("%s:\n got %s\nwant %s", target, got, exp)
		}
	}
	// Not found: the current generation alone, and a missing object.
	for _, target := range []string{objA + "missing.txt", objA + "dir%2Fa%20b.txt?generation=" + strconv.FormatInt(put.Generation-1, 10)} {
		body := gcsErr(t, e.do("GET", target, tokenA, nil), 404, "notFound")
		if !strings.HasPrefix(body.Message, "No such object: "+bucketA+"/") {
			t.Errorf("%s: %q", target, body.Message)
		}
	}
	gcsErr(t, e.do("GET", objA+"dir%2Fa%20b.txt?generation=abc", tokenA, nil), 400, "invalid")
	// Preconditions on the object read.
	gen := strconv.FormatInt(put.Generation, 10)
	if rec := e.do("GET", objA+"dir%2Fa%20b.txt?ifGenerationMatch="+gen+"&ifMetagenerationMatch=1", tokenA, nil); rec.Code != 200 {
		t.Errorf("matching preconditions: %d %s", rec.Code, rec.Body)
	}
	for _, q := range []string{"ifGenerationMatch=0", "ifGenerationNotMatch=" + gen, "ifMetagenerationMatch=2", "ifMetagenerationNotMatch=1"} {
		gcsErr(t, e.do("GET", objA+"dir%2Fa%20b.txt?"+q, tokenA, nil), 412, "conditionNotMet")
	}
	// An object without metadata or tokens has no metadata key, and no contentEncoding.
	e.put(e.a, "plain", "", core.Patch{})
	rec := e.do("GET", objA+"plain", tokenA, nil)
	if strings.Contains(rec.Body.String(), `"metadata"`) || strings.Contains(rec.Body.String(), "contentEncoding") ||
		!strings.Contains(rec.Body.String(), `"size":"0"`) {
		t.Errorf("plain object: %s", rec.Body)
	}
}

func listNames(t *testing.T, body []byte) (names, prefixes []string, next string) {
	t.Helper()
	var l wire.GCSObjects
	if err := json.Unmarshal(body, &l); err != nil || l.Kind != "storage#objects" {
		t.Fatalf("list %s: %v", body, err)
	}
	for _, o := range l.Items {
		names = append(names, o.Name)
	}
	return names, l.Prefixes, l.NextPageToken
}

// objects.list: Go's empty parameters are absent, prefixes and items page together, and what
// v0.3 does not list is 400.
func TestListObjects(t *testing.T) {
	e := newEnv(t)
	for _, n := range []string{"a/1", "a/2", "a/b/3", "c", "d//e"} {
		e.put(e.a, n, "x", core.Patch{})
	}
	e.put(e.b, "a/other-project", "x", core.Patch{})
	list := "/storage/v1/b/" + bucketA + "/o"
	goQuery := "?alt=json&delimiter=%2F&endOffset=&includeFoldersAsPrefixes=false&includeTrailingDelimiter=false&matchGlob=&pageToken=&prefix=a%2F&prettyPrint=false&projection=full&startOffset=&versions=false"
	rec := e.do("GET", list+goQuery, tokenA, nil)
	names, prefixes, next := listNames(t, rec.Body.Bytes())
	if !slices.Equal(names, []string{"a/1", "a/2"}) || !slices.Equal(prefixes, []string{"a/b/"}) || next != "" {
		t.Errorf("Go's list: %v %v %q", names, prefixes, next)
	}
	// Node, without a delimiter: every name, an empty segment kept on GCS.
	names, _, _ = listNames(t, e.do("GET", list+"?", tokenA, nil).Body.Bytes())
	if !slices.Equal(names, []string{"a/1", "a/2", "a/b/3", "c", "d//e"}) {
		t.Errorf("Node's list: %v", names)
	}
	// Pages.
	var all []string
	token := ""
	for range 10 {
		target := list + "?maxResults=2"
		if token != "" {
			target += "&pageToken=" + token
		}
		var page []string
		page, _, token = listNames(t, e.do("GET", target, tokenA, nil).Body.Bytes())
		all = append(all, page...)
		if token == "" {
			break
		}
	}
	if len(all) != 5 {
		t.Errorf("paged: %v", all)
	}
	names, _, _ = listNames(t, e.do("GET", list+"?startOffset=a%2F2&endOffset=c", tokenA, nil).Body.Bytes())
	if !slices.Equal(names, []string{"a/2", "a/b/3"}) {
		t.Errorf("offsets: %v", names)
	}
	// An empty list is {"kind":"storage#objects"}.
	if rec := e.do("GET", list+"?prefix=zzz", tokenA, nil); rec.Body.String() != `{"kind":"storage#objects"}` {
		t.Errorf("empty list: %s", rec.Body)
	}
	for _, q := range []string{"versions=true", "includeTrailingDelimiter=true", "includeFoldersAsPrefixes=true",
		"softDeleted=true", "matchGlob=**.txt", "maxResults=-1", "maxResults=x", "versions=yes", "pageToken=forged"} {
		gcsErr(t, e.do("GET", list+"?"+q, tokenA, nil), 400, "invalid")
	}
	// B's account on A's list, and nobody.
	gcsErr(t, e.do("GET", list, tokenB, nil), 403, "forbidden")
	gcsErr(t, e.do("GET", list, "", nil), 401, "required")
}

// objects.patch: null deletes a field, metadata merges key by key, "metadata": null clears it
// (and the tokens with it, ADR-023 §3-48), the tokens are set through metadata, and the
// metageneration goes up.
func TestPatchObject(t *testing.T) {
	e := newEnv(t)
	put := e.put(e.a, "p.txt", "hello", core.Patch{
		Fields:   map[string]*string{core.FieldContentType: new("text/plain"), core.FieldCacheControl: new("no-store")},
		Metadata: map[string]*string{"keep": new("1"), "drop": new("2")},
	})
	patch := func(body string, q string) wire.GCSObject {
		t.Helper()
		rec := e.do("PATCH", objA+"p.txt?alt=json&prettyPrint=false&projection=full"+q, tokenA, strings.NewReader(body),
			"Content-Type", "application/json")
		if rec.Code != 200 {
			t.Fatalf("PATCH %s: %d %s", body, rec.Code, rec.Body)
		}
		return decodeObject(t, rec.Body.Bytes())
	}
	o := patch(`{"cacheControl":null,"contentLanguage":"ar","metadata":{"drop":null,"add":"3","firebaseStorageDownloadTokens":"t1, t2"},"generation":"7","size":"9"}`, "")
	if o.CacheControl != "" || o.ContentLanguage != "ar" || o.ContentType != "text/plain" || o.Metageneration != "2" ||
		o.Generation != strconv.FormatInt(put.Generation, 10) || o.Size != "5" ||
		!maps.Equal(o.Metadata, map[string]string{"keep": "1", "add": "3", "firebaseStorageDownloadTokens": "t1,t2"}) {
		t.Errorf("after the first patch: %+v", o)
	}
	o = patch(`{"metadata":{"firebaseStorageDownloadTokens":null}}`, "&ifMetagenerationMatch=2")
	if o.Metageneration != "3" || !maps.Equal(o.Metadata, map[string]string{"keep": "1", "add": "3"}) {
		t.Errorf("tokens removed: %+v", o.Metadata)
	}
	o = patch(`{"metadata":null}`, "&generation="+strconv.FormatInt(put.Generation, 10))
	if o.Metadata != nil || o.Metageneration != "4" {
		t.Errorf("metadata cleared: %+v", o)
	}
	gcsErr(t, e.do("PATCH", objA+"p.txt?ifMetagenerationMatch=1", tokenA, strings.NewReader(`{}`)), 412, "conditionNotMet")
	gcsErr(t, e.do("PATCH", objA+"p.txt?generation=1", tokenA, strings.NewReader(`{}`)), 404, "notFound")
	gcsErr(t, e.do("PATCH", objA+"p.txt?generation=1&ifGenerationMatch=1", tokenA, strings.NewReader(`{}`)), 404, "notFound")
	gcsErr(t, e.do("PATCH", objA+"missing", tokenA, strings.NewReader(`{}`)), 404, "notFound")
	for _, body := range []string{`[1]`, `{"contentType":5}`, `{"metadata":{"a":1}}`, `{"metadata":[]}`, `not json`} {
		gcsErr(t, e.do("PATCH", objA+"p.txt", tokenA, strings.NewReader(body)), 400, "invalid")
	}
	for _, body := range []string{`{"acl":[{"entity":"allUsers","role":"READER"}]}`, `{"temporaryHold":true}`,
		`{"customTime":"2026-01-01T00:00:00Z"}`, `{"storageClass":"NEARLINE"}`} {
		gcsErr(t, e.do("PATCH", objA+"p.txt", tokenA, strings.NewReader(body)), 501, "notImplemented")
	}
	// The empty values of those fields, as a resource read back carries them, change nothing.
	if rec := e.do("PATCH", objA+"p.txt", tokenA, strings.NewReader(`{"acl":[],"temporaryHold":false,"storageClass":"STANDARD","kind":"storage#object"}`)); rec.Code != 200 {
		t.Errorf("empty deferred fields: %d %s", rec.Code, rec.Body)
	}
	gcsErr(t, e.do("PATCH", objA+"p.txt", tokenA, strings.NewReader(`{"contentType":"`+strings.Repeat("x", 70<<10)+`"}`)), 413, "invalid")
	gcsErr(t, e.do("PATCH", objA+"p.txt", tokenB, strings.NewReader(`{}`)), 403, "forbidden")
	gcsErr(t, e.do("PUT", objA+"p.txt", tokenA, strings.NewReader(`{}`)), 501, "notImplemented")
}

// objects.delete: 204 without a body; preconditions and ?generation= as for a change.
func TestDeleteObject(t *testing.T) {
	e := newEnv(t)
	put := e.put(e.a, "d.txt", "x", core.Patch{})
	gen := strconv.FormatInt(put.Generation, 10)
	gcsErr(t, e.do("DELETE", objA+"d.txt?generation=1", tokenA, nil), 404, "notFound")
	gcsErr(t, e.do("DELETE", objA+"d.txt?ifGenerationMatch=1", tokenA, nil), 412, "conditionNotMet")
	gcsErr(t, e.do("DELETE", objA+"d.txt", tokenB, nil), 403, "forbidden")
	gcsErr(t, e.do("DELETE", objA+"d.txt", "", nil), 401, "required")
	rec := e.do("DELETE", objA+"d.txt?alt=json&prettyPrint=false&generation="+gen+"&ifMetagenerationMatch=1", tokenA, nil)
	if rec.Code != 204 || rec.Body.Len() != 0 {
		t.Fatalf("delete: %d %q", rec.Code, rec.Body)
	}
	gcsErr(t, e.do("DELETE", objA+"d.txt", tokenA, nil), 404, "notFound")
	if _, err := e.svc.Stat(context.Background(), e.a, "d.txt", core.Principal{Kind: core.PrincipalAdmin}, core.SurfaceGCS, 0); err == nil {
		t.Fatal("the object is still there")
	}
}

// rewriteTo copies in one request and says done:true, without a rewriteToken: Node's copy and
// move ({} body), and Go's CopierFrom with attributes.
func TestRewrite(t *testing.T) {
	e := newEnv(t)
	src := e.put(e.a, "src.txt", "hello world", core.Patch{
		Fields:   map[string]*string{core.FieldContentType: new("text/plain"), core.FieldCacheControl: new("no-store")},
		Metadata: map[string]*string{"added": new("1")},
		Tokens:   &[]string{"tok"},
	})
	rw := func(dest, q, body string) (wire.GCSRewrite, map[string]any) {
		t.Helper()
		rec := e.do("POST", objA+"src.txt/rewriteTo/b/"+bucketA+"/o/"+dest+q, tokenA, strings.NewReader(body),
			"Content-Type", "application/json")
		if rec.Code != 200 {
			t.Fatalf("rewrite to %s: %d %s", dest, rec.Code, rec.Body)
		}
		var r wire.GCSRewrite
		var raw map[string]any
		if json.Unmarshal(rec.Body.Bytes(), &r) != nil || json.Unmarshal(rec.Body.Bytes(), &raw) != nil {
			t.Fatalf("rewrite reply %s", rec.Body)
		}
		return r, raw
	}
	r, raw := rw("copy%2Fone.txt", "?", "{}")
	if !r.Done || r.Kind != "storage#rewriteResponse" || r.TotalBytesRewritten != "11" || r.ObjectSize != "11" ||
		raw["rewriteToken"] != nil || r.Resource == nil || r.Resource.Name != "copy/one.txt" || r.Resource.ContentType != "text/plain" ||
		r.Resource.CacheControl != "no-store" || r.Resource.CRC32C != wire.CRC32C(src.CRC32C) ||
		!maps.Equal(r.Resource.Metadata, map[string]string{"added": "1", "firebaseStorageDownloadTokens": "tok"}) {
		t.Errorf("Node's copy: %+v %v", r.Resource, raw)
	}
	// Go's copier with attributes: they replace the destination's metadata.
	r, _ = rw("two.txt", "?alt=json&prettyPrint=false&projection=full&ifGenerationMatch=0&maxBytesRewrittenPerCall=1048576",
		`{"contentType":"application/json","metadata":{"x":"y"}}`)
	if r.Resource.ContentType != "application/json" || r.Resource.CacheControl != "" || !maps.Equal(r.Resource.Metadata, map[string]string{"x": "y"}) {
		t.Errorf("Go's copy with attributes: %+v", r.Resource)
	}
	gen := strconv.FormatInt(src.Generation, 10)
	rw("three.txt", "?sourceGeneration="+gen+"&ifSourceMetagenerationMatch=1", "")
	target := objA + "src.txt/rewriteTo/b/" + bucketA + "/o/"
	gcsErr(t, e.do("POST", target+"two.txt?ifGenerationMatch=0", tokenA, strings.NewReader("{}")), 412, "conditionNotMet")
	gcsErr(t, e.do("POST", target+"four.txt?sourceGeneration=1", tokenA, strings.NewReader("{}")), 404, "notFound")
	gcsErr(t, e.do("POST", target+"four.txt?ifSourceGenerationMatch=1", tokenA, strings.NewReader("{}")), 412, "conditionNotMet")
	// The source replaced after the caller saw it: its old generation is not copied in its place
	// (core checks the conditions on the row whose file it copies).
	next := e.put(e.a, "src.txt", "replaced", core.Patch{})
	gcsErr(t, e.do("POST", target+"five.txt?sourceGeneration="+gen, tokenA, strings.NewReader("{}")), 404, "notFound")
	for _, q := range []string{"ifSourceGenerationMatch=" + gen, "ifSourceGenerationNotMatch=" + strconv.FormatInt(next.Generation, 10),
		"ifSourceMetagenerationMatch=2", "ifSourceMetagenerationNotMatch=1"} {
		gcsErr(t, e.do("POST", target+"five.txt?"+q, tokenA, strings.NewReader("{}")), 412, "conditionNotMet")
	}
	if _, err := e.svc.Stat(context.Background(), e.a, "five.txt", core.Principal{Kind: core.PrincipalAdmin}, core.SurfaceGCS, 0); err == nil {
		t.Error("a refused rewrite made its destination")
	}
	r, _ = rw("five.txt", "?ifSourceGenerationMatch="+strconv.FormatInt(next.Generation, 10), "{}")
	if r.Resource.CRC32C != wire.CRC32C(next.CRC32C) {
		t.Errorf("the current source: %+v", r.Resource)
	}
	gcsErr(t, e.do("POST", target+"four.txt?rewriteToken=abc", tokenA, strings.NewReader("{}")), 400, "invalid")
	gcsErr(t, e.do("POST", target+"four.txt?destinationPredefinedAcl=publicRead", tokenA, strings.NewReader("{}")), 501, "notImplemented")
	body := gcsErr(t, e.do("POST", objA+"missing/rewriteTo/b/"+bucketA+"/o/x", tokenA, strings.NewReader("{}")), 404, "notFound")
	if body.Message != wire.MessageNoSuchObject(bucketA, "missing") {
		t.Errorf("missing source: %q", body.Message)
	}
	gcsErr(t, e.do("POST", target+"x", "", strings.NewReader("{}")), 401, "required")
	gcsErr(t, e.do("POST", objA+"src.txt/rewriteTo/b/nobody-app.aruma.test/o/x", tokenA, strings.NewReader("{}")), 404, "notFound")
	gcsErr(t, e.do("POST", objA+"src.txt/rewriteTo/b/"+bucketA+"/o", tokenA, strings.NewReader("{}")), 404, "notFound")
	gcsErr(t, e.do("POST", objA+"src.txt/copyTo/b/"+bucketA+"/o/x", tokenA, strings.NewReader("{}")), 501, "notImplemented")
	gcsErr(t, e.do("POST", objA+"src.txt/compose", tokenA, strings.NewReader("{}")), 501, "notImplemented")
}

// A service account works on its own project's bucket alone: on another project's it is 403,
// a rewrite into another project's bucket included, and nothing there changes.
func TestServiceAccountIsBoundToItsProject(t *testing.T) {
	e := newEnv(t)
	e.put(e.a, "a.txt", "alpha", core.Patch{})
	e.put(e.b, "b.txt", "bravo", core.Patch{})
	objB := "/storage/v1/b/" + bucketB + "/o/"
	for _, c := range []struct{ method, target, body string }{
		{"GET", "/storage/v1/b/" + bucketB, ""},
		{"PATCH", "/storage/v1/b/" + bucketB, `{"cors":[]}`},
		{"GET", objB, ""},
		{"GET", objB + "b.txt", ""},
		{"GET", objB + "b.txt?alt=media", ""},
		{"PATCH", objB + "b.txt", `{"metadata":{"x":"y"}}`},
		{"DELETE", objB + "b.txt", ""},
		{"POST", objB + "b.txt/rewriteTo/b/" + bucketA + "/o/stolen.txt", "{}"},
		{"POST", objA + "a.txt/rewriteTo/b/" + bucketB + "/o/planted.txt", "{}"},
		{"POST", "/upload/storage/v1/b/" + bucketB + "/o?uploadType=media&name=x", "x"},
	} {
		body := gcsErr(t, e.do(c.method, c.target, tokenA, strings.NewReader(c.body)), http.StatusForbidden, "forbidden")
		if !strings.HasPrefix(body.Message, emailA+" does not have storage.") {
			t.Errorf("%s %s: %q", c.method, c.target, body.Message)
		}
	}
	// The 403 comes before any object is looked up (ADR-022 §14, storage's exception to S-11): an
	// object that exists and one that does not get the same reply, byte for byte.
	for _, c := range []struct{ method, exists, missing, body string }{
		{"GET", objB + "b.txt", objB + "missing.txt", ""},
		{"GET", objB + "b.txt?alt=media", objB + "missing.txt?alt=media", ""},
		{"PATCH", objB + "b.txt", objB + "missing.txt", `{"metadata":{"x":"y"}}`},
		{"DELETE", objB + "b.txt", objB + "missing.txt", ""},
		{"DELETE", objB + "b.txt?generation=1", objB + "missing.txt?generation=1", ""},
		{"GET", objB + "?prefix=b.txt", objB + "?prefix=missing", ""},
		{"POST", objB + "b.txt/rewriteTo/b/" + bucketA + "/o/x.txt", objB + "missing.txt/rewriteTo/b/" + bucketA + "/o/x.txt", "{}"},
		{"POST", objA + "a.txt/rewriteTo/b/" + bucketB + "/o/b.txt", objA + "a.txt/rewriteTo/b/" + bucketB + "/o/missing.txt", "{}"},
		{"POST", objA + "a.txt/rewriteTo/b/" + bucketB + "/o/b.txt?ifGenerationMatch=0", objA + "a.txt/rewriteTo/b/" + bucketB + "/o/missing.txt?ifGenerationMatch=0", "{}"},
	} {
		one := e.do(c.method, c.exists, tokenA, strings.NewReader(c.body))
		other := e.do(c.method, c.missing, tokenA, strings.NewReader(c.body))
		if one.Code != 403 || one.Code != other.Code || one.Body.String() != other.Body.String() {
			t.Errorf("%s: an existing object %d %s, a missing one %d %s", c.method, one.Code, one.Body, other.Code, other.Body)
		}
	}
	admin := core.Principal{Kind: core.PrincipalAdmin}
	ctx := context.Background()
	if _, err := e.svc.Stat(ctx, e.b, "planted.txt", admin, core.SurfaceGCS, 0); err == nil {
		t.Error("A's account planted an object in B")
	}
	if _, err := e.svc.Stat(ctx, e.a, "stolen.txt", admin, core.SurfaceGCS, 0); err == nil {
		t.Error("A's account copied B's object")
	}
	if o, err := e.svc.Stat(ctx, e.b, "b.txt", admin, core.SurfaceGCS, 0); err != nil || o.Metageneration != 1 {
		t.Errorf("B's object: %+v, %v", o, err)
	}
}
