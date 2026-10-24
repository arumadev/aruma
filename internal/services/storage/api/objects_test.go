package api

import (
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/arumadev/aruma/internal/services/storage/core"
)

// What a PATCH body becomes (A §3.5, §7; ADR-022 §9): the bodies the three SDKs send, iOS's
// nulls for the fields it read back, and the client's attempts at the tokens.
func TestReadPatch(t *testing.T) {
	a := New(routeService(t), Options{}, nil)
	s := func(v string) *string { return &v }
	type want struct {
		fields   map[string]*string
		metadata map[string]*string
		clear    bool
		status   int // 0: accepted
	}
	for _, c := range []struct {
		name, body string
		want       want
	}{
		{"empty", "", want{}},
		{"empty object", "{}", want{}},
		{"JS: set and delete", `{"cacheControl":"no-cache","contentType":null,"metadata":{"keep":"1","drop":null}}`,
			want{fields: map[string]*string{"cacheControl": s("no-cache"), "contentType": nil},
				metadata: map[string]*string{"keep": s("1"), "drop": nil}}},
		{"iOS: the fields it read back come as null and are ignored",
			`{"bucket":"","name":"a.txt","size":5,"downloadTokens":null,"etag":null,"crc32c":null,"md5Hash":null,` +
				`"generation":null,"metageneration":null,"timeCreated":null,"updated":null,"storageClass":null,"contentLanguage":"en"}`,
			want{fields: map[string]*string{"contentLanguage": s("en")}}},
		{"metadata null clears", `{"metadata":null}`, want{clear: true}},
		{"a planted token is dropped", `{"metadata":{"firebaseStorageDownloadTokens":"mine","k":"v"}}`,
			want{metadata: map[string]*string{"k": s("v")}}},
		{"a token removal is dropped", `{"metadata":{"firebaseStorageDownloadTokens":null}}`,
			want{metadata: map[string]*string{}}},
		{"downloadTokens is ignored", `{"downloadTokens":"mine-2"}`, want{}},
		{"all five fields", `{"contentType":"a/b","cacheControl":"c","contentDisposition":"d","contentEncoding":"e","contentLanguage":"f"}`,
			want{fields: map[string]*string{"contentType": s("a/b"), "cacheControl": s("c"), "contentDisposition": s("d"),
				"contentEncoding": s("e"), "contentLanguage": s("f")}}},
		{"a number for a field", `{"contentType":5}`, want{status: 400}},
		{"a number in the metadata", `{"metadata":{"k":5}}`, want{status: 400}},
		{"metadata a list", `{"metadata":["k"]}`, want{status: 400}},
		{"not an object", `["contentType"]`, want{status: 400}},
		{"null body", `null`, want{status: 400}},
		{"not JSON", `contentType=a`, want{status: 400}},
		{"too large", `{"metadata":{"k":"` + strings.Repeat("x", int(core.DefaultLimits().JSONBodyBytes)) + `"}}`, want{status: 413}},
	} {
		r := httptest.NewRequest("PATCH", "/v0/b/x/o/a", strings.NewReader(c.body))
		got, err := a.readPatch(r)
		if c.want.status != 0 {
			status, _ := a.errorStatus(r, err)
			if err == nil || status != c.want.status {
				t.Errorf("%s: %+v, %v (%d), want %d", c.name, got, err, status, c.want.status)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if !sameFields(got.Fields, c.want.fields) || !sameFields(got.Metadata, c.want.metadata) ||
			got.ClearMetadata != c.want.clear || got.Tokens != nil {
			t.Errorf("%s: %s", c.name, describePatch(got))
		}
	}
}

func sameFields(a, b map[string]*string) bool {
	return len(a) == len(b) && maps.EqualFunc(a, b, func(x, y *string) bool {
		return x == nil && y == nil || x != nil && y != nil && *x == *y
	})
}

func describePatch(p core.Patch) string {
	var b strings.Builder
	for _, m := range []map[string]*string{p.Fields, p.Metadata} {
		b.WriteString("{")
		for k, v := range m {
			if v == nil {
				b.WriteString(k + ":null ")
			} else {
				b.WriteString(k + ":" + *v + " ")
			}
		}
		b.WriteString("} ")
	}
	if p.ClearMetadata {
		b.WriteString("clear")
	}
	return b.String()
}

// Every named error of core has its /v0 status, never 409 nor 308. A Retry-After, bounded, goes
// on the 503s of passing pressure alone: on any other, iOS would retry forever (A §5.3).
func TestErrorStatuses(t *testing.T) {
	a := New(routeService(t), Options{}, nil)
	for _, c := range []struct {
		err        error
		status     int
		retryAfter string
	}{
		{core.ErrBucketNotFound, 404, ""}, {core.ErrNotFound, 404, ""}, {core.ErrUploadNotFound, 404, ""},
		{core.ErrPermissionDenied, 403, ""}, {core.ErrUnauthenticated, 401, ""}, {core.ErrQuotaExceeded, 402, ""},
		{core.ErrTooLarge, 413, ""}, {core.ErrSizeMismatch, 400, ""}, {core.ErrChecksumMismatch, 400, ""},
		{core.ErrInvalidPageToken, 400, ""}, {core.ErrInvalidArgument, 400, ""}, {core.ErrUploadFinal, 400, ""},
		{core.ErrUploadCancelled, 400, ""}, {core.ErrTooManyUploads, 429, ""},
		{core.ErrOverloaded, 503, "5"}, {fmt.Errorf("evaluate: %w", core.ErrOverloaded), 503, "5"},
		{errTooManyWrites, 503, "5"},
		{core.ErrUnavailable, 503, ""}, {fmt.Errorf("%w: the database", core.ErrUnavailable), 503, ""},
		{&core.InvalidError{Reason: "invalid", Message: "bad"}, 400, ""},
		{&core.OffsetError{Received: 5}, 400, ""}, {errBadRoute, 400, ""}, {errUnauthenticated, 401, ""},
		// A session's first byte over the live sessions is both, and 402 (core's errTooManyLive).
		{fmt.Errorf("%w: %w", core.ErrQuotaExceeded, core.ErrTooManyUploads), 402, ""},
	} {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/v0/b/x/o", nil)
		a.writeError(rec, r, c.err)
		if rec.Code != c.status {
			t.Errorf("%v: %d, want %d", c.err, rec.Code, c.status)
		}
		if ra := rec.Header().Get("Retry-After"); ra != c.retryAfter {
			t.Errorf("%v: Retry-After %q, want %q", c.err, ra, c.retryAfter)
		}
	}
	// AllowUploadStart's refusal: its wait, bounded to [1s, 60s].
	for wait, want := range map[time.Duration]string{1 << 40: "60", time.Millisecond: "1", 7 * time.Second: "7"} {
		rec := httptest.NewRecorder()
		a.writeRetry(rec, httptest.NewRequest("POST", "/v0/b/x/o", nil), http.StatusServiceUnavailable, wait)
		if rec.Code != 503 || rec.Header().Get("Retry-After") != want {
			t.Errorf("writeRetry(%v): %d %q, want %s", wait, rec.Code, rec.Header().Get("Retry-After"), want)
		}
	}
}
