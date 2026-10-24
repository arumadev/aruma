package wire

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"runtime"
	"strings"
	"testing"
)

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }

func readAll(t *testing.T, r io.Reader) []byte {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// related builds a body the way the web SDK does (A §3.9): CRLF lines and no CRLF after the
// closing boundary.
func related(boundary, json, mediaHeaders, media string) string {
	return "--" + boundary + "\r\nContent-Type: application/json; charset=utf-8\r\n\r\n" + json +
		"\r\n--" + boundary + "\r\n" + mediaHeaders + "\r\n" + media + "\r\n--" + boundary + "--"
}

func TestRelatedReader(t *testing.T) {
	const ct = "multipart/related; boundary=b0undary"
	for _, c := range []struct {
		name, ct, body string
		maxJSON        int64
		json, typ      string
		media          string
		err            bool // ErrBadMultipart, from NewRelatedReader or from Media
	}{
		{name: "web SDK", ct: ct, body: related("b0undary", `{"name":"a"}`, "Content-Type: text/plain\r\n", "hello"),
			json: `{"name":"a"}`, typ: "text/plain", media: "hello"},
		{name: "closing CRLF", ct: ct, body: related("b0undary", `{}`, "Content-Type: image/png\r\n", "\x89PNG\r\n") + "\r\n",
			json: `{}`, typ: "image/png", media: "\x89PNG\r\n"},
		{name: "epilogue", ct: ct, body: related("b0undary", `{}`, "", "x") + "\r\nignored epilogue",
			json: `{}`, media: "x"},
		{name: "no media type", ct: ct, body: related("b0undary", `{}`, "", "x"), json: `{}`, media: "x"},
		{name: "empty media", ct: ct, body: related("b0undary", `{}`, "Content-Type: text/plain\r\n", ""),
			json: `{}`, typ: "text/plain"},
		{name: "media holding a boundary-like line", ct: ct,
			body: related("b0undary", `{}`, "", "--b0undaryX\r\n--b0und\r\n--b0undary-"), json: `{}`,
			media: "--b0undaryX\r\n--b0und\r\n--b0undary-"},
		{name: "preamble", ct: ct, body: "preamble\r\n" + related("b0undary", `{}`, "", "x"), json: `{}`, media: "x"},
		{name: "quoted boundary", ct: `multipart/related; boundary="a b:c"`,
			body: related("a b:c", `{}`, "", "x"), json: `{}`, media: "x"},
		{name: "LF lines", ct: ct,
			body: "--b0undary\nContent-Type: application/json\n\n{}\n--b0undary\n\nx\n--b0undary--", json: `{}`, media: "x"},
		{name: "JSON at its cap", ct: ct, body: related("b0undary", `{"a":1}`, "", "x"), maxJSON: 7,
			json: `{"a":1}`, media: "x"},
		{name: "JSON over its cap", ct: ct, body: related("b0undary", `{"a":12}`, "", "x"), maxJSON: 7, err: true},
		{name: "not related", ct: "multipart/form-data; boundary=b0undary", body: related("b0undary", `{}`, "", "x"), err: true},
		{name: "no boundary", ct: "multipart/related", body: related("b0undary", `{}`, "", "x"), err: true},
		{name: "boundary too long", ct: "multipart/related; boundary=" + strings.Repeat("b", 71),
			body: related(strings.Repeat("b", 71), `{}`, "", "x"), err: true},
		{name: "boundary ending in a space", ct: `multipart/related; boundary="b "`, body: related("b ", `{}`, "", "x"), err: true},
		{name: "boundary with a bad char", ct: `multipart/related; boundary="b;b"`, body: related("b;b", `{}`, "", "x"), err: true},
		{name: "wrong boundary", ct: "multipart/related; boundary=other", body: related("b0undary", `{}`, "", "x"), err: true},
		{name: "first part not JSON", ct: ct,
			body: "--b0undary\r\nContent-Type: text/plain\r\n\r\n{}\r\n--b0undary\r\n\r\nx\r\n--b0undary--", err: true},
		{name: "first part untyped", ct: ct,
			body: "--b0undary\r\n\r\n{}\r\n--b0undary\r\n\r\nx\r\n--b0undary--", err: true},
		{name: "one part", ct: ct,
			body: "--b0undary\r\nContent-Type: application/json\r\n\r\n{}\r\n--b0undary--", err: true},
		{name: "three parts", ct: ct,
			body: "--b0undary\r\nContent-Type: application/json\r\n\r\n{}\r\n--b0undary\r\n\r\nx\r\n--b0undary\r\n\r\ny\r\n--b0undary--", err: true},
		{name: "cut in the media", ct: ct,
			body: "--b0undary\r\nContent-Type: application/json\r\n\r\n{}\r\n--b0undary\r\n\r\nxyz", err: true},
		{name: "cut at the closing delimiter", ct: ct,
			body: "--b0undary\r\nContent-Type: application/json\r\n\r\n{}\r\n--b0undary\r\n\r\nx\r\n--b0undary", err: true},
		{name: "quoted-printable media", ct: ct,
			body: related("b0undary", `{}`, "Content-Transfer-Encoding: quoted-printable\r\n", "a=3Db"), err: true},
		{name: "base64 JSON", ct: ct,
			body: "--b0undary\r\nContent-Type: application/json\r\nContent-Transfer-Encoding: base64\r\n\r\ne30=\r\n--b0undary\r\n\r\nx\r\n--b0undary--", err: true},
		{name: "binary media", ct: ct, body: related("b0undary", `{}`, "Content-Transfer-Encoding: binary\r\n", "\x00\xff"),
			json: `{}`, media: "\x00\xff"},
		{name: "empty body", ct: ct, body: "", err: true},
	} {
		maxJSON := c.maxJSON
		if maxJSON == 0 {
			maxJSON = 1 << 10
		}
		rr, err := NewRelatedReader(strings.NewReader(c.body), c.ct, maxJSON)
		var media []byte
		if err == nil {
			media, err = io.ReadAll(rr.Media)
		}
		if c.err {
			if !errors.Is(err, ErrBadMultipart) {
				t.Errorf("%s: %v, want ErrBadMultipart", c.name, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if string(rr.JSON) != c.json || rr.MediaType != c.typ || string(media) != c.media {
			t.Errorf("%s: JSON %q type %q media %q", c.name, rr.JSON, rr.MediaType, media)
		}
	}
}

// The body's own errors (a size cap, a broken connection) come through as they are, from the
// headers and from the media.
func TestRelatedReaderBodyErrors(t *testing.T) {
	boom := errors.New("connection reset")
	const ct = "multipart/related; boundary=b"
	full := related("b", `{"name":"a"}`, "", strings.Repeat("x", 100<<10))
	for _, cut := range []int{0, 10, 40, 60, 5000, len(full) - 3} {
		r := io.MultiReader(strings.NewReader(full[:cut]), errReader{boom})
		rr, err := NewRelatedReader(r, ct, 1<<10)
		if err == nil {
			_, err = io.ReadAll(rr.Media)
		}
		if !errors.Is(err, boom) {
			t.Errorf("cut at %d: %v, want the body's error", cut, err)
		}
	}
	// http.MaxBytesReader's error, which the surface turns into 413.
	r := http.MaxBytesReader(nil, io.NopCloser(strings.NewReader(full)), 50<<10)
	rr, err := NewRelatedReader(r, ct, 1<<10)
	if err == nil {
		_, err = io.ReadAll(rr.Media)
	}
	var mbe *http.MaxBytesError
	if !errors.As(err, &mbe) {
		t.Errorf("%v, want *http.MaxBytesError", err)
	}
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

// counter is an endless or long source that counts what is read from it: what a parser reads
// bounds what it can hold.
type counter struct {
	r    io.Reader
	read int64
}

func (c *counter) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.read += int64(n)
	return n, err
}

// repeat is an endless reader of one pattern.
type repeat struct {
	pattern []byte
	off     int
}

func (r *repeat) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = r.pattern[r.off]
		r.off = (r.off + 1) % len(r.pattern)
	}
	return len(p), nil
}

// A hostile body cannot make the parser read, and so hold, more than its budgets: endless
// headers, an endless preamble, an endless header line, a JSON part without end, a third part
// with endless headers. Each is refused after a bounded read.
func TestRelatedReaderBounded(t *testing.T) {
	const ct = "multipart/related; boundary=b"
	const maxJSON = 64 << 10
	start := "--b\r\nContent-Type: application/json\r\n"
	for _, c := range []struct {
		name  string
		body  io.Reader
		media bool // the failure comes from reading Media
	}{
		{"endless preamble lines", &repeat{pattern: []byte("preamble\r\n")}, false},
		{"endless preamble line", &repeat{pattern: []byte("x")}, false},
		{"endless JSON part headers", io.MultiReader(strings.NewReader(start), &repeat{pattern: []byte("X-A: b\r\n")}), false},
		{"endless JSON header line", io.MultiReader(strings.NewReader(start+"X-A: "), &repeat{pattern: []byte("b")}), false},
		{"endless JSON part", io.MultiReader(strings.NewReader(start+"\r\n"), &repeat{pattern: []byte("{")}), false},
		{"endless media headers", io.MultiReader(strings.NewReader(start+"\r\n{}\r\n--b\r\n"),
			&repeat{pattern: []byte("X-A: b\r\n")}), false},
		{"endless third part headers", io.MultiReader(strings.NewReader(start+"\r\n{}\r\n--b\r\n\r\nx\r\n--b\r\n"),
			&repeat{pattern: []byte("X-A: b\r\n")}), true},
		{"endless parts", io.MultiReader(strings.NewReader(start+"\r\n{}\r\n--b\r\n\r\nx"),
			&repeat{pattern: []byte("\r\n--b\r\n\r\nx")}), true},
	} {
		src := &counter{r: c.body}
		rr, err := NewRelatedReader(src, ct, maxJSON)
		if c.media {
			if err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			_, err = io.Copy(io.Discard, rr.Media)
		}
		if !errors.Is(err, ErrBadMultipart) {
			t.Errorf("%s: %v, want ErrBadMultipart", c.name, err)
		}
		if limit := int64(maxJSON + 3*headerBudget + 3*4096); src.read > limit {
			t.Errorf("%s: read %d bytes, over %d", c.name, src.read, limit)
		}
	}
}

// A media part of any size streams through a fixed amount of memory: the reader holds no more
// than its look-ahead, however large the media.
func TestRelatedReaderStreams(t *testing.T) {
	const size = 64 << 20
	body := io.MultiReader(strings.NewReader("--b\r\nContent-Type: application/json\r\n\r\n{}\r\n--b\r\n\r\n"),
		io.LimitReader(&repeat{pattern: []byte("0123456789\r\n--")}, size), strings.NewReader("\r\n--b--"))
	rr, err := NewRelatedReader(body, "multipart/related; boundary=b", 1<<10)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 32<<10)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	n, err := io.CopyBuffer(io.Discard, struct{ io.Reader }{rr.Media}, buf)
	runtime.ReadMemStats(&after)
	if err != nil || n != size {
		t.Fatalf("%d %v", n, err)
	}
	if spent := after.TotalAlloc - before.TotalAlloc; spent > 256<<10 {
		t.Errorf("%d bytes allocated to stream 64 MiB", spent)
	}
}

// Every multipart body the clients sent parses: the web SDK's (no CRLF after the closing
// boundary), Node's and Go's (mime/multipart). The emulator refused the one with a boundary that
// is not the body's (raw_upload.json "multipart, wrong boundary"), and so does the reader. The
// media checksums are checked against the server's in TestGoldenUploadChecksums.
func TestGoldenMultipart(t *testing.T) {
	var ok, refused int
	for _, e := range exchanges(t) {
		h := header(e.Request.Headers)
		ct := h.Get("Content-Type")
		if !strings.HasPrefix(ct, "multipart/related") {
			continue
		}
		raw, whole := e.Request.Body.bytes()
		if !whole {
			t.Fatalf("%v: body not recorded", e)
		}
		rr, err := NewRelatedReader(bytes.NewReader(raw), ct, 1<<20)
		if err == nil {
			_, err = io.ReadAll(rr.Media)
		}
		if e.step == "multipart, wrong boundary" {
			if !errors.Is(err, ErrBadMultipart) {
				t.Errorf("%v: %v", e, err)
			}
			refused++
			continue
		}
		if err != nil {
			t.Errorf("%v: %v", e, err)
			continue
		}
		if !bytes.HasPrefix(rr.JSON, []byte("{")) {
			t.Errorf("%v: JSON %q", e, rr.JSON)
		}
		ok++
	}
	if ok < 35 || refused != 1 {
		t.Errorf("%d parsed, %d refused", ok, refused)
	}
}
