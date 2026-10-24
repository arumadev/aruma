package wire

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

// The parsers' fuzz targets (storage-layout.md, S2-B). Each holds a parser to never panic, to
// keep its result's invariants, and to read no more than its bounds; S2-B ran each ten minutes
// (go test -fuzz=<target> -fuzztime=10m -parallel=2, GOMEMLIMIT=1GiB).

func FuzzSplitObjectPath(f *testing.F) {
	for _, s := range []string{"b/x", "b/x/o", "b/x/o/", "b/x/o/a%2Fb%20c", "b/x/o/a/rewriteTo/b/y/o/c%2Fd",
		"b/x/acl", "b//o", "b/%zz", "b/x/o/%", "b/x/o/%D9%85+1"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		p, err := SplitObjectPath(s)
		if err != nil {
			if !errors.Is(err, ErrBadPath) || p != (ObjectPath{}) {
				t.Fatalf("%q: %+v %v", s, p, err)
			}
			return
		}
		if p.Bucket == "" || len(p.Bucket)+len(p.Object)+len(p.Rest) > len(s) {
			t.Fatalf("%q: %+v", s, p)
		}
		if !p.Objects && p.Object != "" {
			t.Fatalf("%q: an object without /o: %+v", s, p)
		}
		// Escaped again, the path splits to the same thing.
		again := "b/" + EscapeComponent(p.Bucket)
		switch {
		case p.Objects:
			again += "/o/" + EscapeComponent(p.Object)
			if p.Rest != "" {
				again += "/" + p.Rest
			}
		case p.Rest != "":
			again += "/" + p.Rest
		}
		q, err := SplitObjectPath(again)
		if err != nil || q != p {
			t.Fatalf("%q: %+v, escaped again %q: %+v %v", s, p, again, q, err)
		}
	})
}

func FuzzContentRange(f *testing.F) {
	for _, s := range []string{"bytes 0-4194303/*", "bytes 0-12/13", "bytes */9437184", "bytes 0-*/*",
		"bytes 5-*/10", "bytes */*", "bytes 5-4/10", "bytes 0-9223372036854775808/*", "Bytes 0-0/1"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		r, err := ParseContentRange(s)
		if err != nil {
			if !errors.Is(err, ErrBadContentRange) {
				t.Fatalf("%q: %v", s, err)
			}
			return
		}
		switch {
		case r.Start < 0:
			if r.End != -1 || r.Open || r.Total < -1 {
				t.Fatalf("%q: %+v", s, r)
			}
		case r.Open:
			if r.End != -1 || (r.Total >= 0 && r.Start > r.Total) {
				t.Fatalf("%q: %+v", s, r)
			}
		default:
			if r.End < r.Start || (r.Total >= 0 && r.End >= r.Total) || r.Total < -1 {
				t.Fatalf("%q: %+v", s, r)
			}
		}
		if back, err := ParseContentRange(r.String()); err != nil || back != r {
			t.Fatalf("%q: %+v writes back as %q: %+v %v", s, r, r.String(), back, err)
		}
	})
}

func FuzzUploadCommand(f *testing.F) {
	for _, s := range []string{"start", "upload", "upload, finalize", "finalize", "query", "cancel", "bogus",
		"upload,,finalize", " upload ,\tfinalize ", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		c, err := ParseUploadCommand(s)
		if err != nil {
			if !errors.Is(err, ErrBadCommand) || c != 0 {
				t.Fatalf("%q: %v %v", s, c, err)
			}
			return
		}
		if c == 0 || c >= 1<<len(commandNames) {
			t.Fatalf("%q: %b", s, c)
		}
		if back, err := ParseUploadCommand(c.String()); err != nil || back != c {
			t.Fatalf("%q: %v writes back as %q: %v %v", s, c, c.String(), back, err)
		}
	})
}

func FuzzRange(f *testing.F) {
	for _, s := range []string{"bytes=2-5", "bytes=0-", "bytes=-4", "bytes=-0", "bytes=64-", "bytes=0-1,4-5",
		"bytes=5-4", "BYTES= 1-2 ", "bytes=99999999999999999999-", "items=0-1"} {
		f.Add(s, int64(64))
		f.Add(s, int64(0))
	}
	f.Fuzz(func(t *testing.T, s string, size int64) {
		r, ok, err := ParseRange(s, size)
		switch {
		case err != nil:
			if !errors.Is(err, ErrUnsatisfiable) || ok || size <= 0 {
				t.Fatalf("%q/%d: %+v %v %v", s, size, r, ok, err)
			}
		case ok:
			if size <= 0 || r.Start < 0 || r.Length < 1 || r.Start >= size || r.Length > size-r.Start {
				t.Fatalf("%q/%d: %+v", s, size, r)
			}
			// What the 206 says is the range served.
			cr := r.ContentRange(size)
			span := cr[len("bytes "):strings.IndexByte(cr, '/')]
			if back, ok, err := ParseRange("bytes="+span, size); !ok || err != nil || back != r {
				t.Fatalf("%q/%d: %+v, its Content-Range %q reads %+v %v %v", s, size, r, cr, back, ok, err)
			}
		default:
			if r != (Range{}) {
				t.Fatalf("%q/%d: %+v without ok", s, size, r)
			}
		}
	})
}

// FuzzRelated reads a multipart body to its end under a fixed cap: no panic, the JSON within its
// cap, every failure ErrBadMultipart, and never more read from the body than the media and the
// parser's budgets.
func FuzzRelated(f *testing.F) {
	const ct = "multipart/related; boundary=b0undary"
	f.Add(ct, []byte(related("b0undary", `{"name":"a"}`, "Content-Type: text/plain\r\n", "hello")))
	f.Add(ct, []byte(related("b0undary", `{}`, "", "--b0undaryX\r\n--b0und")+"\r\n"))
	f.Add(ct, []byte("--b0undary\nContent-Type: application/json\n\n{}\n--b0undary\n\nx\n--b0undary--"))
	f.Add(ct, []byte("--b0undary\r\nContent-Type: application/json\r\n\r\n{}\r\n--b0undary\r\n\r\nx\r\n--b0undary\r\n\r\ny\r\n--b0undary--"))
	f.Add(`multipart/related; boundary="a b:c"`, []byte(related("a b:c", `{}`, "Content-Transfer-Encoding: binary\r\n", "\x00")))
	f.Add("multipart/related; boundary=other", []byte(related("b0undary", `{}`, "", "x")))
	f.Add("multipart/mixed; boundary=b0undary", []byte(related("b0undary", `{}`, "", "x")))
	f.Fuzz(func(t *testing.T, contentType string, body []byte) {
		const maxJSON = 1 << 10
		src := &counter{r: bytes.NewReader(body)}
		rr, err := NewRelatedReader(src, contentType, maxJSON)
		if err != nil {
			if !errors.Is(err, ErrBadMultipart) || rr != nil {
				t.Fatalf("%v", err)
			}
			return
		}
		if len(rr.JSON) > maxJSON {
			t.Fatalf("JSON of %d bytes", len(rr.JSON))
		}
		n, err := io.Copy(io.Discard, rr.Media)
		if err != nil && !errors.Is(err, ErrBadMultipart) {
			t.Fatalf("media: %v", err)
		}
		if n > int64(len(body)) || src.read > int64(len(body)) {
			t.Fatalf("read %d, media %d, of a body of %d", src.read, n, len(body))
		}
	})
}
