package wire

import (
	"bytes"
	"errors"
	"io"
	"math"
	"strings"
	"testing"
)

// The parsers' fuzz targets (storage-layout.md, S2-B, and FuzzHashHeader from its review). Each holds a parser to never panic, to
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
		"bytes 5-*/10", "bytes */*", "bytes 5-4/10", "bytes 0-9223372036854775808/*", "Bytes 0-0/1",
		"bytes 0--1/0", "bytes 5-4/5", "bytes 5-3/5", "bytes 0-9223372036854775807/*"} {
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
			if r.End < r.Start || r.End == math.MaxInt64 || (r.Total >= 0 && r.End >= r.Total) || r.Total < -1 {
				t.Fatalf("%q: %+v", s, r)
			}
		}
		if n := r.Length(); n < -1 || (r.Open && n != -1) || (!r.Open && r.Start < 0 && n != 0) ||
			(!r.Open && r.Start >= 0 && n < 1) {
			t.Fatalf("%q: %+v has length %d", s, r, n)
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
		// The body as it is: every failure is ErrBadMultipart, and the media is within it.
		rr, err := NewRelatedReader(bytes.NewReader(body), contentType, maxJSON)
		if err != nil {
			if !errors.Is(err, ErrBadMultipart) || rr != nil {
				t.Fatalf("%v", err)
			}
		} else {
			if len(rr.JSON) > maxJSON {
				t.Fatalf("JSON of %d bytes", len(rr.JSON))
			}
			n, err := io.Copy(io.Discard, rr.Media)
			if err != nil && !errors.Is(err, ErrBadMultipart) {
				t.Fatalf("media: %v", err)
			}
			if n > int64(len(body)) {
				t.Fatalf("media of %d bytes from a body of %d", n, len(body))
			}
		}
		if len(body) == 0 {
			return
		}
		// The body repeated without end: before the media, the parser reads no more than its
		// budgets, which NewRelatedReader sets in turn (headerBudget for the preamble and the
		// JSON part's headers, then maxJSON+1+headerBudget for the JSON and the media part's
		// headers), each a cap on reads, not on bytes kept.
		src := &counter{r: io.MultiReader(bytes.NewReader(body), &repeat{pattern: body})}
		rr, err = NewRelatedReader(src, contentType, maxJSON)
		if limit := int64(maxJSON + 1 + 2*headerBudget); src.read > limit {
			t.Fatalf("read %d bytes before the media, over %d", src.read, limit)
		}
		if err == nil {
			// The media streams without a cap of its own; its caller sets one.
			if _, err := io.CopyN(io.Discard, rr.Media, 1<<20); err != nil && err != io.EOF && !errors.Is(err, ErrBadMultipart) {
				t.Fatalf("media: %v", err)
			}
		}
	})
}

func FuzzHashHeader(f *testing.F) {
	for _, s := range []string{"", "crc32c=U5UsBg==", "crc32c=U5UsBg==,md5=XUFAKrxLKna5cZ2REBfFkg==",
		"crc32c=U5UsBg==, md5=XUFAKrxLKna5cZ2REBfFkg==", "crc32c=U5UsBg==\nmd5=XUFAKrxLKna5cZ2REBfFkg==",
		"crc32c=U5UsBg==,crc32c=U5UsBg==", "sha256=x", "crc32c=", "md5=AAAA", "crc32c=AAAAAB=="} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		values := strings.Split(s, "\n") // one header value per line
		h, err := ParseHashHeader(values)
		if err != nil {
			if !errors.Is(err, ErrBadHash) || h != (Hashes{}) {
				t.Fatalf("%q: %+v %v", s, h, err)
			}
			return
		}
		// What was read writes back to the same checksums.
		var entries []string
		if h.HasCRC32C {
			entries = append(entries, "crc32c="+CRC32C(h.CRC32C))
		}
		if h.HasMD5 {
			entries = append(entries, "md5="+MD5(h.MD5))
		}
		if back, err := ParseHashHeader([]string{strings.Join(entries, ",")}); len(entries) > 0 && (err != nil || back != h) {
			t.Fatalf("%q: %+v writes back as %v: %+v %v", s, h, entries, back, err)
		}
	})
}
