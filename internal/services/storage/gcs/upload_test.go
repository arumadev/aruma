package gcs

import (
	"bufio"
	"bytes"
	"context"
	"crypto/md5"
	"encoding/json"
	"errors"
	"hash/crc32"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/arumadev/aruma/internal/services/storage/core"
	"github.com/arumadev/aruma/internal/services/storage/wire"
)

// The uploads of S4-B, request by request as Node 8.4.0 and Go 1.62.1 send them (B §5).

const upload = "/upload/storage/v1/b/" + bucketA + "/o"

func crc(s string) string {
	return wire.CRC32C(crc32.Checksum([]byte(s), crc32.MakeTable(crc32.Castagnoli)))
}
func md5b64(s string) string { return wire.MD5(md5.Sum([]byte(s))) }

// gcsObject decodes an object resource and checks the checksums the server computed for body.
func gcsObject(t *testing.T, rec *httptest.ResponseRecorder, body string) wire.GCSObject {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var o wire.GCSObject
	if err := json.Unmarshal(rec.Body.Bytes(), &o); err != nil {
		t.Fatalf("decode %s: %v", rec.Body, err)
	}
	if o.Kind != wire.KindObject || o.Size != strconv.Itoa(len(body)) || o.CRC32C != crc(body) || o.MD5Hash != md5b64(body) {
		t.Fatalf("object %s, want size %d crc32c %s md5 %s", rec.Body, len(body), crc(body), md5b64(body))
	}
	return o
}

// stored returns the object core holds, or nil.
func (e *env) stored(b core.Bucket, name string) *core.Object {
	e.t.Helper()
	o, err := e.svc.Stat(context.Background(), b, name, core.Principal{Kind: core.PrincipalAdmin}, core.SurfaceGCS, 0)
	if errors.Is(err, core.ErrNotFound) {
		return nil
	}
	if err != nil {
		e.t.Fatal(err)
	}
	return &o
}

// send runs r through the handler.
func (e *env) send(r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	return rec
}

// related builds a multipart/related body as TEENY writes it for Node (no CRLF after the
// closing boundary) or as Go's mime/multipart does (with it).
func related(js, mediaType, data string, trailingCRLF bool) (string, string) {
	const boundary = "f3c1a2b4-5d6e-4f70-8a9b-0c1d2e3f4a5b"
	var b strings.Builder
	b.WriteString("--" + boundary + "\r\nContent-Type: application/json\r\n\r\n" + js + "\r\n")
	b.WriteString("--" + boundary + "\r\n")
	if mediaType != "" {
		b.WriteString("Content-Type: " + mediaType + "\r\n")
	}
	b.WriteString("\r\n" + data + "\r\n--" + boundary + "--")
	if trailingCRLF {
		b.WriteString("\r\n")
	}
	return "multipart/related; boundary=" + boundary, b.String()
}

func TestUploadMedia(t *testing.T) {
	e := newEnv(t)
	rec := e.do("POST", upload+"?uploadType=media&name=dir%2Fa+b.txt", tokenA, strings.NewReader("hello aruma\n"),
		"Content-Type", "text/plain")
	o := gcsObject(t, rec, "hello aruma\n")
	if o.Name != "dir/a b.txt" || o.ContentType != "text/plain" || o.Bucket != bucketA || o.ContentDisposition != "" {
		t.Fatalf("object %+v", o)
	}
	// Without a Content-Type the object is application/octet-stream; without a name, 400.
	o = gcsObject(t, e.do("POST", upload+"?uploadType=media&name=raw", tokenA, strings.NewReader("x")), "x")
	if o.ContentType != "application/octet-stream" {
		t.Fatalf("content type %q", o.ContentType)
	}
	gcsErr(t, e.do("POST", upload+"?uploadType=media", tokenA, strings.NewReader("x")), 400, wire.ReasonRequired)
	// Without uploadType the upload is media (CT, gcs.endpoints.test.ts:158-173; ADR-023 §3-91).
	if o = gcsObject(t, e.do("POST", upload+"?name=plain.txt", tokenA, strings.NewReader("hello world")), "hello world"); o.Name != "plain.txt" {
		t.Fatalf("name %q", o.Name)
	}
	gcsErr(t, e.do("POST", upload, tokenA, strings.NewReader("x")), 400, wire.ReasonRequired)
}

func TestUploadMultipart(t *testing.T) {
	e := newEnv(t)
	// Node's resumable:false: the name in the query alone, the JSON without it, the media part
	// typed, and no CRLF after the closing boundary.
	ct, body := related(`{"contentType":"text/plain","metadata":{"k":"v"},"cacheControl":"public, max-age=60"}`,
		"text/plain", "node bytes", false)
	rec := e.do("POST", upload+"?name=node.txt&uploadType=multipart", tokenA, strings.NewReader(body), "Content-Type", ct)
	o := gcsObject(t, rec, "node bytes")
	if o.Name != "node.txt" || o.ContentType != "text/plain" || o.Metadata["k"] != "v" || o.CacheControl != "public, max-age=60" {
		t.Fatalf("node object %+v", o)
	}
	// Go under 16 MiB: the name in both, its own crc32c in the JSON, the type in the media part.
	ct, body = related(`{"bucket":"`+bucketA+`","name":"go.bin","crc32c":"`+crc("go bytes")+`"}`,
		"application/x-go", "go bytes", true)
	rec = e.do("POST", upload+"?alt=json&name=go.bin&prettyPrint=false&projection=full&uploadType=multipart", tokenA,
		strings.NewReader(body), "Content-Type", ct)
	if o = gcsObject(t, rec, "go bytes"); o.ContentType != "application/x-go" {
		t.Fatalf("go object %+v", o)
	}
	// The name may come from the JSON alone.
	ct, body = related(`{"name":"json-name.txt"}`, "", "j", false)
	if o = gcsObject(t, e.do("POST", upload+"?uploadType=multipart", tokenA, strings.NewReader(body), "Content-Type", ct), "j"); o.Name != "json-name.txt" {
		t.Fatalf("name %q", o.Name)
	}
	// A body that is not one JSON part then the media is 400, and stores nothing.
	ct, body = related(`{}`, "", "cut", false)
	rec = e.do("POST", upload+"?name=cut.txt&uploadType=multipart", tokenA, strings.NewReader(body[:len(body)-10]), "Content-Type", ct)
	gcsErr(t, rec, 400, wire.ReasonInvalid)
	// A Content-Type that is not multipart/related with a boundary is "Bad content type." (CT,
	// gcs.endpoints.test.ts:320-330), whichever way multipart was asked for.
	for _, rec := range []*httptest.ResponseRecorder{
		e.do("POST", upload+"?name=x&uploadType=multipart", tokenA, strings.NewReader("x"), "Content-Type", "text/plain"),
		e.do("POST", upload+"?name=x", tokenA, strings.NewReader("x"), "Content-Type", "foo", wire.UploadProtocol, "multipart"),
		e.do("POST", upload+"?name=x&uploadType=multipart", tokenA, strings.NewReader("x"), "Content-Type", "multipart/related"),
	} {
		if b := gcsErr(t, rec, 400, wire.ReasonInvalid); !strings.HasPrefix(b.Message, "Bad content type.") {
			t.Errorf("message %q", b.Message)
		}
	}
	// With X-Goog-Upload-Protocol: multipart and no uploadType, the body is multipart (CT,
	// gcs.endpoints.test.ts:306-318).
	ct, body = related(`{"name":"by-header.txt"}`, "text/plain", "hello there!", false)
	if o = gcsObject(t, e.do("POST", upload, tokenA, strings.NewReader(body), "Content-Type", ct, wire.UploadProtocol, "multipart"), "hello there!"); o.Name != "by-header.txt" {
		t.Fatalf("name %q", o.Name)
	}
	if e.stored(e.a, "cut.txt") != nil || e.stored(e.a, "x") != nil {
		t.Fatal("a malformed multipart body left an object")
	}
}

// A checksum the client claims that is not the data's is 400 and no object: in the JSON part
// (Go), in X-Goog-Hash, or two claims that contradict each other.
func TestUploadChecksumsThatDifferCreateNothing(t *testing.T) {
	e := newEnv(t)
	cases := []struct {
		name, js string
		header   []string
	}{
		{"crc.json", `{"crc32c":"` + crc("other") + `"}`, nil},
		{"md5.json", `{"md5Hash":"` + md5b64("other") + `"}`, nil},
		{"crc.header", `{}`, []string{"X-Goog-Hash", "crc32c=" + crc("other")}},
		{"md5.header", `{}`, []string{"X-Goog-Hash", "md5=" + md5b64("other")}},
		{"both", `{"crc32c":"` + crc("data") + `"}`, []string{"X-Goog-Hash", "crc32c=" + crc("other")}},
	}
	for _, c := range cases {
		ct, body := related(c.js, "", "data", false)
		rec := e.do("POST", upload+"?uploadType=multipart&name="+c.name, tokenA, strings.NewReader(body),
			append([]string{"Content-Type", ct}, c.header...)...)
		gcsErr(t, rec, 400, wire.ReasonInvalid)
		if e.stored(e.a, c.name) != nil {
			t.Errorf("%s: the object was stored", c.name)
		}
	}
	gcsErr(t, e.do("POST", upload+"?uploadType=media&name=bad", tokenA, strings.NewReader("data"), "X-Goog-Hash", "crc32c=nope"), 400, wire.ReasonInvalid)
	// The right claims pass.
	ct, body := related(`{"crc32c":"`+crc("data")+`","md5Hash":"`+md5b64("data")+`"}`, "", "data", false)
	gcsObject(t, e.do("POST", upload+"?uploadType=multipart&name=good", tokenA, strings.NewReader(body),
		"Content-Type", ct, "X-Goog-Hash", "crc32c="+crc("data")+",md5="+md5b64("data")), "data")
}

func TestUploadIfGenerationMatchZeroOnAnExistingObject(t *testing.T) {
	e := newEnv(t)
	old := e.put(e.a, "taken.txt", "old", core.Patch{})
	ct, body := related(`{}`, "", "new", false)
	gcsErr(t, e.do("POST", upload+"?uploadType=multipart&name=taken.txt&ifGenerationMatch=0", tokenA, strings.NewReader(body),
		"Content-Type", ct), 412, wire.ReasonConditionNotMet)
	gcsErr(t, e.do("POST", upload+"?uploadType=media&name=taken.txt&ifGenerationMatch=0", tokenA, strings.NewReader("new")),
		412, wire.ReasonConditionNotMet)
	// Through a session the precondition is checked at finalize, and ends the session.
	loc := e.start(t, "taken.txt", "&ifGenerationMatch=0", `{}`)
	rec := e.chunk(t, "PUT", loc, "bytes 0-*/*", "new", nil)
	gcsErr(t, rec, 412, wire.ReasonConditionNotMet)
	gcsErr(t, e.chunk(t, "PUT", loc, "bytes */*", "", nil), 412, wire.ReasonConditionNotMet)
	if cur := e.stored(e.a, "taken.txt"); cur == nil || cur.Generation != old.Generation {
		t.Fatalf("the object changed: %+v", cur)
	}
	// On a name that is free it holds; ifGenerationMatch=<current> replaces.
	gcsObject(t, e.do("POST", upload+"?uploadType=media&name=free.txt&ifGenerationMatch=0", tokenA, strings.NewReader("new")), "new")
	gcsObject(t, e.do("POST", upload+"?uploadType=media&name=taken.txt&ifGenerationMatch="+strconv.FormatInt(old.Generation, 10),
		tokenA, strings.NewReader("newer")), "newer")
	gcsErr(t, e.do("POST", upload+"?uploadType=media&name=x&ifGenerationMatch=-1", tokenA, strings.NewReader("new")), 400, wire.ReasonInvalid)
}

// start begins a resumable upload as Node does and returns the session URL as a request target.
func (e *env) start(t *testing.T, name, query, js string, header ...string) string {
	t.Helper()
	rec := e.do("POST", upload+"?name="+url.QueryEscape(name)+"&uploadType=resumable"+query, tokenA, strings.NewReader(js),
		append([]string{"Content-Type", "application/json"}, header...)...)
	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 || !strings.HasPrefix(loc, baseURL+upload+"?") {
		t.Fatalf("start: %d %q: %s", rec.Code, loc, rec.Body)
	}
	u, err := url.Parse(loc)
	if err != nil || u.Query().Get("upload_id") == "" || u.Query().Get("name") != name || u.Query().Get("uploadType") != "resumable" {
		t.Fatalf("Location %q", loc)
	}
	return strings.TrimPrefix(loc, baseURL)
}

// chunk sends a request on a session, without Authorization, with Content-Range cr ("" for
// none) and the body; header adds headers, such as Go's X-GUploader-No-308.
func (e *env) chunk(t *testing.T, method, target, cr, body string, header []string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	if cr != "" {
		r.Header.Set("Content-Range", cr)
	}
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Set(header[i], header[i+1])
	}
	return e.send(r)
}

func TestResumableStartMetadata(t *testing.T) {
	e := newEnv(t)
	// Node: contentType moved to X-Upload-Content-Type, the body {} or the rest of the metadata.
	loc := e.start(t, "node.txt", "", `{"metadata":{"k":"v"},"cacheControl":"no-cache"}`,
		wire.XUploadContentType, "text/plain", wire.XUploadContentLength, "5")
	o := gcsObject(t, e.chunk(t, "PUT", loc, "bytes 0-*/*", "12345", nil), "12345")
	if o.ContentType != "text/plain" || o.Metadata["k"] != "v" || o.CacheControl != "no-cache" {
		t.Fatalf("object %+v", o)
	}
	// Go: the JSON's contentType wins over the sniffed one in the header.
	loc = e.start(t, "go.txt", "", `{"bucket":"`+bucketA+`","name":"go.txt","contentType":"text/csv"}`,
		wire.XUploadContentType, "text/plain; charset=utf-8")
	if o = gcsObject(t, e.chunk(t, "PUT", loc, "bytes 0-1/2", "ab", nil), "ab"); o.ContentType != "text/csv" {
		t.Fatalf("content type %q", o.ContentType)
	}
	// Without either, application/octet-stream.
	loc = e.start(t, "none", "", ``)
	if o = gcsObject(t, e.chunk(t, "PUT", loc, "bytes 0-0/1", "z", nil), "z"); o.ContentType != "application/octet-stream" {
		t.Fatalf("content type %q", o.ContentType)
	}
	// The declared size holds: more bytes than declared end the session with 400.
	loc = e.start(t, "declared", "", `{}`, wire.XUploadContentLength, "3")
	gcsErr(t, e.chunk(t, "PUT", loc, "bytes 0-*/*", "four", nil), 400, wire.ReasonInvalid)
	gcsErr(t, e.do("POST", upload+"?name=x&uploadType=resumable", tokenA, strings.NewReader(""), wire.XUploadContentLength, "-1"), 400, wire.ReasonInvalid)
	gcsErr(t, e.do("POST", upload+"?uploadType=resumable", tokenA, strings.NewReader("{}")), 400, wire.ReasonRequired)
	gcsErr(t, e.do("POST", upload+"?name=x&uploadType=resumable", tokenA, strings.NewReader("[1]")), 400, wire.ReasonInvalid)
}

// Node's save(): one PUT with Content-Range: bytes 0-*/* and a chunked body, an empty file too.
func TestResumableNodeSingleRequest(t *testing.T) {
	e := newEnv(t)
	loc := e.start(t, "single.txt", "", `{}`, wire.XUploadContentType, "text/plain")
	r := httptest.NewRequest("PUT", loc, iotest.HalfReader(strings.NewReader("hello aruma\n")))
	r.ContentLength = -1 // chunked
	r.Header.Set("Content-Range", "bytes 0-*/*")
	r.Header.Set("Authorization", "Bearer anything") // ignored on a session
	o := gcsObject(t, e.send(r), "hello aruma\n")
	if o.Name != "single.txt" || o.ContentType != "text/plain" {
		t.Fatalf("object %+v", o)
	}
	// A repeat of the request, as after a lost reply, answers the same object.
	if again := gcsObject(t, e.chunk(t, "PUT", loc, "bytes */*", "", nil), "hello aruma\n"); again.Generation != o.Generation {
		t.Fatalf("the repeat answers generation %s, not %s", again.Generation, o.Generation)
	}
	loc = e.start(t, "empty.txt", "", `{}`)
	gcsObject(t, e.chunk(t, "PUT", loc, "bytes 0-*/*", "", nil), "")
}

// Node with chunkSize: each chunk but the last is answered 308 with Range and no Location; a
// status query too; the last chunk may be empty (bytes N-(N-1)/N).
func TestResumableNodeChunks(t *testing.T) {
	e := newEnv(t)
	loc := e.start(t, "chunks.bin", "", `{}`)
	rec := e.chunk(t, "PUT", loc, "bytes */*", "", nil)
	if rec.Code != http.StatusPermanentRedirect || rec.Header().Get("Range") != "" || rec.Header().Get("Location") != "" {
		t.Fatalf("query before a byte: %d %v", rec.Code, rec.Header())
	}
	for i, c := range []struct{ cr, body, rng string }{
		{"bytes 0-3/*", "abcd", "bytes=0-3"},
		{"bytes 4-7/*", "efgh", "bytes=0-7"},
		{"bytes 4-7/*", "efgh", "bytes=0-7"}, // a resend after a lost reply changes nothing
		{"bytes */*", "", "bytes=0-7"},
	} {
		rec := e.chunk(t, "PUT", loc, c.cr, c.body, nil)
		if rec.Code != http.StatusPermanentRedirect || rec.Header().Get("Range") != c.rng || rec.Header().Get("Location") != "" ||
			rec.Body.Len() != 0 || rec.Header().Get(wire.HTTPStatusCodeOverride) != "" {
			t.Fatalf("%d %s: %d %v %s", i, c.cr, rec.Code, rec.Header(), rec.Body)
		}
	}
	o := gcsObject(t, e.chunk(t, "PUT", loc, "bytes 8-9/10", "ij", nil), "abcdefghij")
	if o.Name != "chunks.bin" {
		t.Fatalf("object %+v", o)
	}
	// The empty last chunk, when the object ends on a chunk's boundary.
	loc = e.start(t, "boundary.bin", "", `{}`)
	if rec := e.chunk(t, "PUT", loc, "bytes 0-3/*", "abcd", nil); rec.Code != http.StatusPermanentRedirect {
		t.Fatalf("chunk: %d", rec.Code)
	}
	gcsObject(t, e.chunk(t, "PUT", loc, "bytes 4-3/4", "", nil), "abcd")
	loc = e.start(t, "empty-chunked.bin", "", `{}`)
	gcsObject(t, e.chunk(t, "PUT", loc, "bytes 0--1/0", "", nil), "")
}

// Go: chunks are POST to the session URL, which carries uploadType=resumable, with
// X-GUploader-No-308: yes; the reply that is not final is 200 with
// X-Http-Status-Code-Override: 308. The last request carries X-Goog-Hash, and is empty when the
// object ends on a chunk's boundary (bytes */N).
func TestResumableGoChunks(t *testing.T) {
	e := newEnv(t)
	go308 := []string{wire.GUploaderNo308, "yes", "Authorization", "Bearer " + tokenB} // anyone's Bearer is ignored
	loc := e.start(t, "go.bin", "", `{"name":"go.bin","bucket":"`+bucketA+`"}`, wire.XUploadContentType, "application/octet-stream")
	for i, c := range []struct{ cr, body, rng string }{
		{"bytes 0-3/*", "abcd", "bytes=0-3"},
		{"bytes 0-3/*", "abcd", "bytes=0-3"}, // Go retries a chunk whole, at its offset
		{"bytes 4-7/*", "efgh", "bytes=0-7"},
	} {
		rec := e.chunk(t, "POST", loc, c.cr, c.body, go308)
		if rec.Code != http.StatusOK || rec.Header().Get(wire.HTTPStatusCodeOverride) != "308" ||
			rec.Header().Get("Range") != c.rng || rec.Body.Len() != 0 || rec.Header().Get("Location") != "" {
			t.Fatalf("%d %s: %d %v %s", i, c.cr, rec.Code, rec.Header(), rec.Body)
		}
	}
	// A chunk past the bytes kept is a gap: 400, and the session stays as it was.
	gcsErr(t, e.chunk(t, "POST", loc, "bytes 12-15/*", "mnop", go308), 400, wire.ReasonInvalid)
	// A Content-Length other than the range's is refused before anything is kept.
	gcsErr(t, e.chunk(t, "POST", loc, "bytes 8-11/*", "ijk", go308), 400, wire.ReasonInvalid)
	rec := e.chunk(t, "POST", loc, "bytes 8-11/*", "ijkl", go308)
	if rec.Code != http.StatusOK || rec.Header().Get("Range") != "bytes=0-11" {
		t.Fatalf("after the gap: %d %v", rec.Code, rec.Header())
	}
	o := gcsObject(t, e.chunk(t, "POST", loc, "bytes */12", "", append(go308, "X-Goog-Hash", "crc32c="+crc("abcdefghijkl"))), "abcdefghijkl")
	if o.Name != "go.bin" || o.ContentType != "application/octet-stream" {
		t.Fatalf("object %+v", o)
	}
	// The last chunk with bytes in it.
	loc = e.start(t, "go2.bin", "", `{}`)
	e.chunk(t, "POST", loc, "bytes 0-3/*", "abcd", go308)
	gcsObject(t, e.chunk(t, "POST", loc, "bytes 4-5/6", "ef", append(go308, "X-Goog-Hash", "crc32c="+crc("abcdef"))), "abcdef")
}

// An X-Goog-Hash on the last chunk that is not the data's ends the session with 400, every
// repeat answers the same, and nothing is stored.
func TestResumableChecksumMismatch(t *testing.T) {
	e := newEnv(t)
	go308 := []string{wire.GUploaderNo308, "yes"}
	loc := e.start(t, "bad.bin", "", `{}`)
	e.chunk(t, "POST", loc, "bytes 0-3/*", "abcd", go308)
	gcsErr(t, e.chunk(t, "POST", loc, "bytes 4-5/6", "ef", append(go308, "X-Goog-Hash", "crc32c="+crc("abcdeX"))), 400, wire.ReasonInvalid)
	gcsErr(t, e.chunk(t, "POST", loc, "bytes 4-5/6", "ef", go308), 400, wire.ReasonInvalid)
	// A start that claims a checksum is held to it at finalize.
	loc2 := e.start(t, "bad2.bin", "", `{"md5Hash":"`+md5b64("other")+`"}`)
	gcsErr(t, e.chunk(t, "PUT", loc2, "bytes 0-*/*", "data", nil), 400, wire.ReasonInvalid)
	if e.stored(e.a, "bad.bin") != nil || e.stored(e.a, "bad2.bin") != nil {
		t.Fatal("an object was stored")
	}
	gcsErr(t, e.chunk(t, "POST", loc, "bytes 4-5/6", "ef", append(go308, "X-Goog-Hash", "crc32c")), 400, wire.ReasonInvalid)
}

func TestResumableSessionRefusals(t *testing.T) {
	e := newEnv(t)
	loc := e.start(t, "s.txt", "", `{}`)
	for _, c := range []struct {
		method, target, cr string
		status             int
		reason             string
	}{
		{"PUT", loc, "bytes 0-3", 400, wire.ReasonInvalid},
		{"PUT", loc, "items 0-3/*", 400, wire.ReasonInvalid},
		{"PUT", loc, "bytes 3-1/*", 400, wire.ReasonInvalid},
		{"PUT", strings.Replace(loc, "upload_id=", "upload_id=x", 1), "bytes 0-3/*", 404, wire.ReasonNotFound},
		{"PUT", strings.Replace(loc, "name=s.txt", "name=other.txt", 1), "bytes 0-3/*", 404, wire.ReasonNotFound},
		{"PUT", strings.Replace(loc, bucketA, bucketB, 1), "bytes 0-3/*", 404, wire.ReasonNotFound},
	} {
		gcsErr(t, e.chunk(t, c.method, c.target, c.cr, "abcd", nil), c.status, c.reason)
	}
	gcsErr(t, e.chunk(t, "PUT", loc, "bytes */*", "body", nil), 400, wire.ReasonInvalid)
	// A request without Content-Range is the whole object.
	gcsObject(t, e.chunk(t, "PUT", loc, "", "whole", nil), "whole")

	// DELETE cancels: 499 as GCS answers, again 499, and the session is gone for chunks.
	loc = e.start(t, "c.txt", "", `{}`)
	e.chunk(t, "PUT", loc, "bytes 0-3/*", "abcd", nil)
	for range 2 {
		if rec := e.chunk(t, "DELETE", loc, "", "", nil); rec.Code != statusClientClosed {
			t.Fatalf("cancel: %d %s", rec.Code, rec.Body)
		}
	}
	gcsErr(t, e.chunk(t, "PUT", loc, "bytes 4-5/6", "ef", nil), 410, wire.ReasonNotFound)
	if u, err := e.svc.Usage(context.Background(), e.a); err != nil || u.ReservedBytes != 0 {
		t.Fatalf("usage after the cancel: %+v %v", u, err)
	}
}

// A start is taken from the client's address against upload_starts_per_minute_per_ip.
func TestUploadStartRate(t *testing.T) {
	e := newEnv(t)
	for i := range 100 {
		r := httptest.NewRequest("POST", upload+"?uploadType=media&name=r", strings.NewReader("x"))
		r.RemoteAddr = "192.0.2.7:4000"
		r.Header.Set("Authorization", "Bearer "+tokenA)
		rec := e.send(r)
		if rec.Code == http.StatusTooManyRequests {
			gcsErr(t, rec, 429, "rateLimitExceeded")
			if rec.Header().Get("Retry-After") == "" || i < e.svc.Limits().UploadStartsPerMinutePerIP {
				t.Fatalf("refused at %d: %v", i, rec.Header())
			}
			return
		}
	}
	t.Fatal("no start was refused")
}

// The body's read error says why an upload failed: over the entry point's cap 413, any other
// read failure 400; nothing is stored.
func TestUploadBodyErrors(t *testing.T) {
	e := newEnv(t)
	r := httptest.NewRequest("POST", upload+"?uploadType=media&name=big", nil)
	r.Header.Set("Authorization", "Bearer "+tokenA)
	rec := httptest.NewRecorder()
	r.Body = http.MaxBytesReader(rec, io.NopCloser(bytes.NewReader(make([]byte, 64))), 16)
	r.ContentLength = -1
	e.h.ServeHTTP(rec, r)
	gcsErr(t, rec, 413, "uploadTooLarge")

	r = httptest.NewRequest("POST", upload+"?uploadType=media&name=broken", io.NopCloser(iotest.ErrReader(errors.New("connection reset"))))
	r.Header.Set("Authorization", "Bearer "+tokenA)
	r.ContentLength = -1
	gcsErr(t, e.send(r), 400, wire.ReasonInvalid)
	if e.stored(e.a, "big") != nil || e.stored(e.a, "broken") != nil {
		t.Fatal("an object was stored")
	}
}

// Retry-After goes with real pressure alone (the start rate, core.ErrOverloaded): a 503 of the
// server's own failure has none, or clients that honour it would retry without a limit.
func TestUploadUnavailableHasNoRetryAfter(t *testing.T) {
	e := newEnv(t)
	loc := e.start(t, "late.txt", "", `{}`)
	if err := e.svc.Close(); err != nil {
		t.Fatal(err)
	}
	for _, rec := range []*httptest.ResponseRecorder{
		e.do("POST", upload+"?uploadType=media&name=closed.txt", tokenA, strings.NewReader("x")),
		e.chunk(t, "PUT", loc, "bytes 0-*/*", "late", nil),
	} {
		gcsErr(t, rec, 503, wire.ReasonBackendError)
		if v := rec.Header().Get("Retry-After"); v != "" {
			t.Errorf("Retry-After %q on a plain 503", v)
		}
	}
	rec := httptest.NewRecorder()
	e.api.writeError(rec, core.ErrOverloaded, bucketA, "x")
	if rec.Code != 503 || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("overloaded: %d %v", rec.Code, rec.Header())
	}
}

// A body slower than min_bytes_per_second is cut at its window's end, and the 408 reaches the
// client on a real connection: the refusal gets a write deadline of its own.
func TestUploadTooSlowIs408(t *testing.T) {
	e := newEnv(t)
	defer func(w time.Duration) { uploadWindow = w }(uploadWindow)
	uploadWindow = 300 * time.Millisecond
	srv := httptest.NewServer(e.h)
	defer srv.Close()
	loc := e.start(t, "slow-session.bin", "", `{}`)
	for _, target := range []string{upload + "?uploadType=media&name=slow.bin", loc} {
		conn, err := net.Dial("tcp", srv.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		method, extra := "POST", "Authorization: Bearer "+tokenA+"\r\n"
		if target == loc {
			method, extra = "PUT", "Content-Range: bytes 0-*/*\r\n"
		}
		// Three bytes, then nothing: far under 4 KiB in the window.
		_, err = io.WriteString(conn, method+" "+target+" HTTP/1.1\r\nHost: aruma.example\r\n"+extra+
			"Transfer-Encoding: chunked\r\n\r\n3\r\nabc\r\n")
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatalf("%s: no reply: %v", target, err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = conn.Close()
		if resp.StatusCode != http.StatusRequestTimeout || !strings.Contains(string(b), `"code":408`) {
			t.Fatalf("%s: %d %s", target, resp.StatusCode, b)
		}
	}
	if e.stored(e.a, "slow.bin") != nil {
		t.Fatal("the slow upload was stored")
	}
}

// dropAfter sends a request with a chunked body on a real connection, then closes it mid-body.
func dropAfter(t *testing.T, addr, head, chunk string) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, head+"Transfer-Encoding: chunked\r\n\r\n"+
		strconv.FormatInt(int64(len(chunk)), 16)+"\r\n"+chunk+"\r\n"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond) // the server reads the chunk before the connection goes
	_ = conn.Close()
}

// A connection that drops mid-body: a media upload leaves nothing reserved and no object, and a
// resumable session keeps the bytes it synced, announced in Range, and goes on from them.
func TestUploadDroppedConnection(t *testing.T) {
	e := newEnv(t)
	srv := httptest.NewServer(e.h)
	defer srv.Close()
	addr := srv.Listener.Addr().String()
	reserved := func() int64 {
		u, err := e.svc.Usage(context.Background(), e.a)
		if err != nil {
			t.Fatal(err)
		}
		return u.ReservedBytes
	}

	dropAfter(t, addr, "POST "+upload+"?uploadType=media&name=dropped.bin HTTP/1.1\r\nHost: aruma.example\r\n"+
		"Authorization: Bearer "+tokenA+"\r\n", strings.Repeat("m", 64<<10))
	for deadline := time.Now().Add(10 * time.Second); reserved() != 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the dropped media upload still reserves %d bytes", reserved())
		}
	}
	if e.stored(e.a, "dropped.bin") != nil {
		t.Fatal("the dropped media upload was stored")
	}

	loc := e.start(t, "dropped-session.bin", "", `{}`)
	dropAfter(t, addr, "PUT "+loc+" HTTP/1.1\r\nHost: aruma.example\r\nContent-Range: bytes 0-*/*\r\n", "abcdef")
	var rec *httptest.ResponseRecorder
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		rec = e.chunk(t, "PUT", loc, "bytes */*", "", nil)
		if rec.Code == http.StatusPermanentRedirect && rec.Header().Get("Range") == "bytes=0-5" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after the drop: %d Range %q %s", rec.Code, rec.Header().Get("Range"), rec.Body)
		}
	}
	if got := reserved(); got != 6 {
		t.Fatalf("the session reserves %d bytes, want its 6 synced", got)
	}
	gcsObject(t, e.chunk(t, "PUT", loc, "bytes 6-8/9", "ghi", nil), "abcdefghi")
	if got := reserved(); got != 0 {
		t.Fatalf("after the finalize %d bytes stay reserved", got)
	}
}
