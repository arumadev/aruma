package gcs

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/arumadev/aruma/internal/services/storage/core"
)

func TestDecodeAttrs(t *testing.T) {
	for _, c := range []struct {
		body   string
		check  func(objectAttrs) bool
		status int // 0: decodes
	}{
		{"", func(a objectAttrs) bool { return !a.Set && a.Patch.Fields == nil }, 0},
		{"{}", func(a objectAttrs) bool { return !a.Set }, 0},
		{"null", func(a objectAttrs) bool { return !a.Set }, 0},
		// Node's resumable start: contentType moved to X-Upload-Content-Type.
		{`{"name":"a/b.txt","bucket":"x","kind":"storage#object","size":"3"}`, func(a objectAttrs) bool { return !a.Set && a.Name == "a/b.txt" }, 0},
		{`{"contentType":"text/plain","cacheControl":null}`, func(a objectAttrs) bool {
			return a.Set && *a.Patch.Fields["contentType"] == "text/plain" && a.Patch.Fields["cacheControl"] == nil
		}, 0},
		{`{"metadata":null}`, func(a objectAttrs) bool { return a.Set && a.Patch.ClearMetadata && a.Patch.Tokens == nil }, 0},
		{`{"metadata":{"a":"1","b":null}}`, func(a objectAttrs) bool {
			return *a.Patch.Metadata["a"] == "1" && a.Patch.Metadata["b"] == nil && len(a.Patch.Metadata) == 2
		}, 0},
		{`{"metadata":{"firebaseStorageDownloadTokens":" t1 ,,t2"}}`, func(a objectAttrs) bool {
			return a.Patch.Metadata == nil && slices.Equal(*a.Patch.Tokens, []string{"t1", "t2"})
		}, 0},
		{`{"metadata":{"firebaseStorageDownloadTokens":null}}`, func(a objectAttrs) bool { return len(*a.Patch.Tokens) == 0 }, 0},
		// Go's multipart JSON carries the crc32c it computed (B §5.2).
		{`{"name":"x","crc32c":"U5UsBg==","md5Hash":"XUFAKrxLKna5cZ2REBfFkg=="}`, func(a objectAttrs) bool {
			return *a.Want.CRC32C == 0x53952c06 && a.Want.MD5 != nil && !a.Set
		}, 0},
		{`{"crc32c":"bad"}`, nil, 400},
		{`{"crc32c":5}`, nil, 400},
		{`{"md5Hash":"AAAA"}`, nil, 400},
		{`{"name":3}`, nil, 400},
		{`{"metadata":"x"}`, nil, 400},
		// A boolean or number is stored as JSON.stringify writes it (CT, gcs-js-sdk.test.ts:942-951;
		// ADR-023 §3-77); an object or an array is not a value.
		{`{"metadata":{"booleanValue":true,"numberValue":-1,"f":false,"x":1.50,"big":1e21,"small":0.0000001,"tiny":1.5E-7,"z":-0}}`, func(a objectAttrs) bool {
			want := map[string]string{"booleanValue": "true", "numberValue": "-1", "f": "false", "x": "1.5", "big": "1e+21",
				"small": "1e-7", "tiny": "1.5e-7", "z": "0"}
			for k, v := range want {
				if got := a.Patch.Metadata[k]; got == nil || *got != v {
					return false
				}
			}
			return len(a.Patch.Metadata) == len(want)
		}, 0},
		{`{"metadata":{"a":{"b":"c"}}}`, nil, 400},
		{`{"metadata":{"a":["b"]}}`, nil, 400},
		{`{"metadata":{"a":1e400}}`, nil, 400},
		{`{"contentType":["x"]}`, nil, 400},
		{`"x"`, nil, 400},
		{`{"acl":[{"entity":"allUsers"}]}`, nil, 501},
		{`{"eventBasedHold":true}`, nil, 501},
		{`{"retention":{"mode":"Locked"}}`, nil, 501},
		{`{"acl":null,"eventBasedHold":false,"customTime":"","storageClass":"STANDARD"}`, func(a objectAttrs) bool { return !a.Set }, 0},
	} {
		a, err := decodeAttrs([]byte(c.body))
		if c.status != 0 {
			e, ok := errors.AsType[*apiError](err)
			if !ok || e.status != c.status {
				t.Errorf("%s: %v, want %d", c.body, err, c.status)
			}
			continue
		}
		if err != nil || !c.check(a) {
			t.Errorf("%s: %+v, %v", c.body, a, err)
		}
	}
}

// decodeAttrs refuses with this surface's errors alone, whatever the body, and never panics.
func FuzzDecodeAttrs(f *testing.F) {
	for _, s := range []string{`{}`, `{"metadata":{"a":null}}`, `{"crc32c":"AAAAAA=="}`, `{"acl":[]}`, `{"cors":null}`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if _, err := decodeAttrs(data); err != nil {
			if e, ok := errors.AsType[*apiError](err); !ok || (e.status != http.StatusBadRequest && e.status != http.StatusNotImplemented) {
				t.Fatalf("%q: %v", data, err)
			}
		}
		if _, _, err := decodeBucketPatch(data); err != nil {
			if _, ok := errors.AsType[*apiError](err); !ok {
				t.Fatalf("bucket %q: %v", data, err)
			}
		}
	})
}

// The resource of an object without a creation time or content fields still has every number
// as a string, and a bucket's CORS rules go out with GCS's names.
func TestResources(t *testing.T) {
	a := New(nil, Options{PublicURL: "https://x/"}, nil)
	o := a.object(core.Object{Bucket: "b", Name: "a/b", Generation: 5, Metageneration: 2, DownloadTokens: []string{"t1", "t2"}})
	if o.Size != "0" || o.Generation != "5" || o.Metageneration != "2" || o.Metadata[core.TokensKey] != "t1,t2" ||
		o.SelfLink != "https://x/storage/v1/b/b/o/a%2Fb" || o.CRC32C != "AAAAAA==" || o.ContentEncoding != "" {
		t.Errorf("object %+v", o)
	}
	b := a.bucket(core.BucketInfo{Bucket: core.Bucket{Project: 7, Name: "b"}, Metageneration: 17,
		CORS: []core.CORSRule{{Origins: []string{"*"}, Methods: []string{"GET"}, MaxAgeSeconds: 60}}})
	if b.ProjectNumber != "7" || b.ETag != "CBE=" || len(b.CORS) != 1 || b.CORS[0].MaxAgeSeconds != 60 || b.CORS[0].Origin[0] != "*" {
		t.Errorf("bucket %+v", b)
	}
}

// Retry-After goes on passing pressure alone: never on a 503 of the server itself, which a
// client that honours Retry-After would retry with no end (S4-A's review).
func TestRetryAfter(t *testing.T) {
	a := New(nil, Options{}, nil)
	for _, c := range []struct {
		err    error
		status int
		retry  string
	}{
		{core.ErrUnavailable, 503, ""},
		{fmt.Errorf("%w: the database", core.ErrUnavailable), 503, ""},
		{core.ErrOverloaded, 503, "1"},
		{errTooManyWrites, 429, "1"},
		{core.ErrTooManyUploads, 429, ""},
		{core.ErrNotFound, 404, ""},
	} {
		rec := httptest.NewRecorder()
		a.writeError(rec, c.err, "b", "o")
		if rec.Code != c.status || rec.Header().Get("Retry-After") != c.retry {
			t.Errorf("%v: %d Retry-After %q, want %d %q", c.err, rec.Code, rec.Header().Get("Retry-After"), c.status, c.retry)
		}
	}
	// A handler that knows the wait (AllowUploadStart's) keeps its own.
	rec := httptest.NewRecorder()
	rec.Header().Set("Retry-After", "7")
	a.writeError(rec, errTooManyWrites, "", "")
	if rec.Header().Get("Retry-After") != "7" {
		t.Errorf("the handler's Retry-After became %q", rec.Header().Get("Retry-After"))
	}
}
