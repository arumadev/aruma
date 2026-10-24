package wire

import (
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestParseUploadCommand(t *testing.T) {
	for _, c := range []struct {
		in   string
		want UploadCommands
		err  bool
	}{
		{in: "start", want: CommandStart},
		{in: "upload", want: CommandUpload},
		{in: "upload, finalize", want: CommandUpload | CommandFinalize},
		{in: "upload,finalize", want: CommandUpload | CommandFinalize},
		{in: " upload ,\tfinalize ", want: CommandUpload | CommandFinalize},
		{in: "finalize", want: CommandFinalize}, // Android, with an empty body (A §3.11)
		{in: "query", want: CommandQuery},
		{in: "cancel", want: CommandCancel},
		{in: "upload, upload", want: CommandUpload},
		{in: "", err: true},
		{in: " ", err: true},
		{in: ",", err: true},
		{in: "upload,", err: true},
		{in: "upload,,finalize", err: true},
		{in: "bogus", err: true}, // raw_resumable.json
		{in: "Upload", err: true},
		{in: "upload finalize", err: true},
		{in: "upload;finalize", err: true},
	} {
		got, err := ParseUploadCommand(c.in)
		if c.err {
			if !errors.Is(err, ErrBadCommand) {
				t.Errorf("%q: %v %v, want ErrBadCommand", c.in, got, err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%q: %v %v, want %v", c.in, got, err, c.want)
		}
	}
	if s := (CommandUpload | CommandFinalize).String(); s != "upload, finalize" {
		t.Errorf("String = %q", s)
	}
}

// A header of millions of commands is one scan and no allocation.
func TestParseUploadCommandLong(t *testing.T) {
	v := strings.Repeat("upload, ", 1_000_000) + "finalize"
	allocs := testing.AllocsPerRun(3, func() {
		if s, err := ParseUploadCommand(v); err != nil || s != CommandUpload|CommandFinalize {
			t.Fatal(s, err)
		}
	})
	if allocs != 0 {
		t.Errorf("%v allocations", allocs)
	}
}

// Every command a client sent is one the parser reads, and writes back the same, but the
// emulator's "bogus" probe (raw_resumable.json).
func TestGoldenUploadCommands(t *testing.T) {
	n := 0
	for _, e := range exchanges(t) {
		v := header(e.Request.Headers).Get(UploadCommand)
		if v == "" {
			continue
		}
		s, err := ParseUploadCommand(v)
		if v == "bogus" {
			if !errors.Is(err, ErrBadCommand) {
				t.Errorf("%v: %v", e, err)
			}
			continue
		}
		if err != nil || s.String() != v {
			t.Errorf("%v: %q: %v %v", e, v, s, err)
		}
		n++
	}
	if n < 25 {
		t.Errorf("only %d commands", n)
	}
}

func TestParseContentRange(t *testing.T) {
	for _, c := range []struct {
		in   string
		want ContentRange
		err  bool
	}{
		{in: "bytes 0-4194303/*", want: ContentRange{Start: 0, End: 4194303, Total: -1}},    // Node chunk (B §5.5)
		{in: "bytes 0-12/13", want: ContentRange{Start: 0, End: 12, Total: 13}},             // a last chunk
		{in: "bytes */9437184", want: ContentRange{Start: -1, End: -1, Total: 9437184}},     // Go's empty last chunk (B §5.7)
		{in: "bytes 0-*/*", want: ContentRange{Start: 0, End: -1, Open: true, Total: -1}},   // Node save() (B §5.4)
		{in: "bytes 13-*/*", want: ContentRange{Start: 13, End: -1, Open: true, Total: -1}}, // its resume
		{in: "bytes 5-*/10", want: ContentRange{Start: 5, End: -1, Open: true, Total: 10}},
		{in: "bytes 10-*/10", want: ContentRange{Start: 10, End: -1, Open: true, Total: 10}},
		{in: "bytes */*", want: ContentRange{Start: -1, End: -1, Total: -1}}, // Node's query (B §5.6)
		{in: "bytes */0", want: ContentRange{Start: -1, End: -1, Total: 0}},
		{in: "Bytes 0-0/1", want: ContentRange{Start: 0, End: 0, Total: 1}},
		{in: "bytes 0-9223372036854775806/9223372036854775807", want: ContentRange{Start: 0, End: math.MaxInt64 - 1, Total: math.MaxInt64}},
		{in: "", err: true},
		{in: "bytes", err: true},
		{in: "bytes ", err: true},
		{in: "bytes 0-4", err: true},
		{in: "bytes 5-4/10", err: true},
		{in: "bytes 0-10/10", err: true},
		{in: "bytes 11-*/10", err: true},
		{in: "bytes -1-4/10", err: true},
		{in: "bytes 0--4/10", err: true},
		{in: "bytes +0-4/10", err: true},
		{in: "bytes 0-4/-1", err: true},
		{in: "bytes 0-4/1x", err: true},
		{in: "bytes  0-4/10", err: true},
		{in: "bytes=0-4/10", err: true},
		{in: "items 0-4/10", err: true},
		{in: "bytes */", err: true},
		{in: "bytes /10", err: true},
		{in: "bytes *-*/*", err: true},
		{in: "bytes 0-*", err: true},
		{in: "bytes 0-9223372036854775808/*", err: true},
		{in: "bytes 0-4/10/12", err: true},
	} {
		got, err := ParseContentRange(c.in)
		if c.err {
			if !errors.Is(err, ErrBadContentRange) {
				t.Errorf("%q: %+v %v, want ErrBadContentRange", c.in, got, err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%q: %+v %v, want %+v", c.in, got, err, c.want)
		}
		if back, err := ParseContentRange(got.String()); err != nil || back != got {
			t.Errorf("%q writes back as %q: %+v %v", c.in, got.String(), back, err)
		}
	}
}

// Every Content-Range Node sent parses, and writes back the same (admin_node.json,
// admin_gcs_endpoint.json).
func TestGoldenContentRanges(t *testing.T) {
	n := 0
	for _, e := range exchanges(t) {
		v := header(e.Request.Headers).Get("Content-Range")
		if v == "" {
			continue
		}
		r, err := ParseContentRange(v)
		if err != nil || r.String() != v {
			t.Errorf("%v: %q: %+v %v", e, v, r, err)
		}
		n++
	}
	if n < 5 {
		t.Errorf("only %d", n)
	}
}

func TestParseLength(t *testing.T) {
	for _, bad := range []string{"", "-1", "+1", " 1", "1 ", "1.0", "0x10", "9223372036854775808",
		"99999999999999999999", strings.Repeat("0", 20)} {
		if _, err := ParseLength(bad); !errors.Is(err, ErrBadLength) {
			t.Errorf("ParseLength(%q) = %v", bad, err)
		}
	}
	for in, want := range map[string]int64{"0": 0, "007": 7, "716800": 716800, "9223372036854775807": math.MaxInt64} {
		if got, err := ParseLength(in); err != nil || got != want {
			t.Errorf("ParseLength(%q) = %d %v", in, got, err)
		}
	}
}

func TestDeclared(t *testing.T) {
	for _, c := range []struct {
		h      http.Header
		length int64
		bad    bool
		typ    string
	}{
		{h: http.Header{}, length: -1}, // Android declares no size (A §3.10)
		{h: http.Header{"X-Goog-Upload-Header-Content-Length": {"716800"}, "X-Goog-Upload-Header-Content-Type": {"text/plain"}},
			length: 716800, typ: "text/plain"}, // web
		{h: http.Header{"X-Goog-Upload-Content-Length": {"10"}, "X-Goog-Upload-Content-Type": {"image/png"}},
			length: 10, typ: "image/png"}, // iOS
		{h: http.Header{UploadContentLength: {"11"}}, length: 11}, // as Google spells it, set by hand
		{h: http.Header{"X-Goog-Upload-Header-Content-Length": {"1"}, "X-Goog-Upload-Content-Length": {"2"}}, length: 1},
		{h: http.Header{"X-Goog-Upload-Header-Content-Length": {"-5"}}, length: -1, bad: true},
		{h: http.Header{"X-Goog-Upload-Header-Content-Length": {""}}, length: -1, bad: true},
		{h: http.Header{"X-Goog-Upload-Header-Content-Type": {"  "}, "X-Goog-Upload-Content-Type": {"a/b"}}, length: -1, typ: "a/b"},
	} {
		if got := DeclaredLength(c.h); got != c.length {
			t.Errorf("%v: DeclaredLength %d, want %d", c.h, got, c.length)
		}
		if _, err := ParseDeclaredLength(c.h); (err != nil) != c.bad {
			t.Errorf("%v: ParseDeclaredLength %v", c.h, err)
		}
		if got := DeclaredType(c.h); got != c.typ {
			t.Errorf("%v: DeclaredType %q, want %q", c.h, got, c.typ)
		}
	}
}

// Every start the web SDK sent declares what DeclaredLength and DeclaredType read; the session
// statuses the emulator answered are the three constants.
func TestGoldenStarts(t *testing.T) {
	starts := 0
	for _, e := range exchanges(t) {
		h := header(e.Request.Headers)
		if v := header(e.Response.Headers).Get(UploadStatus); v != "" &&
			v != StatusActive && v != StatusFinal && v != StatusCancelled {
			t.Errorf("%v: status %q", e, v)
		}
		if h.Get(UploadCommand) != "start" {
			continue
		}
		want := int64(-1)
		if v := h.Get(UploadHeaderContentLength); v != "" {
			want, _ = strconv.ParseInt(v, 10, 64)
		}
		if got := DeclaredLength(h); got != want {
			t.Errorf("%v: DeclaredLength %d, want %d", e, got, want)
		}
		if got := DeclaredType(h); got != h.Get(UploadHeaderContentType) {
			t.Errorf("%v: DeclaredType %q", e, got)
		}
		starts++
	}
	if starts < 5 {
		t.Errorf("only %d starts", starts)
	}
}

func TestSetUploadStatus(t *testing.T) {
	h := http.Header{}
	SetUploadStatus(h, StatusActive, 262144)
	if h[UploadStatus][0] != "active" || h[UploadSizeReceived][0] != "262144" {
		t.Errorf("%v", h)
	}
	h = http.Header{}
	SetUploadStatus(h, StatusFinal, -1)
	if h[UploadStatus][0] != "final" || len(h[UploadSizeReceived]) != 0 {
		t.Errorf("%v", h)
	}
}

// Go reads 200 with the override and Node a real 308 (B §5.5, §5.7); both get Range only when a
// byte is kept, and never Location or a body.
func TestWriteIncomplete(t *testing.T) {
	for _, c := range []struct {
		no308     string
		received  int64
		status    int
		override  string
		rangeWant string
	}{
		{"yes", 4194304, 200, "308", "bytes=0-4194303"},
		{"Yes", 0, 200, "308", ""},
		{"", 4194304, 308, "", "bytes=0-4194303"},
		{"", 0, 308, "", ""},
		{"no", 1, 308, "", "bytes=0-0"},
	} {
		r := httptest.NewRequest("PUT", "/upload/storage/v1/b/x/o?upload_id=1", nil)
		if c.no308 != "" {
			r.Header.Set("X-GUploader-No-308", c.no308)
		}
		w := httptest.NewRecorder()
		w.Header().Set("Location", "https://elsewhere.test/")
		WriteIncomplete(w, r, c.received)
		res := w.Result()
		if res.StatusCode != c.status || res.Header.Get(HTTPStatusCodeOverride) != c.override ||
			res.Header.Get("Range") != c.rangeWant || res.Header.Get("Location") != "" || w.Body.Len() != 0 {
			t.Errorf("%+v: %d %v %q", c, res.StatusCode, res.Header, w.Body.String())
		}
		if _, ok := res.Header[HTTPStatusCodeOverride]; c.override != "" && !ok {
			t.Errorf("%+v: the override is not written as spelled: %v", c, res.Header)
		}
	}
}
