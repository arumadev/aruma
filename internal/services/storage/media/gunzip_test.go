package media

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func gzipped(t testing.TB, data []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&b, gzip.BestCompression)
	if _, err := zw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func gzipObject(t testing.TB, plain []byte) (Item, []byte) {
	stored := gzipped(t, plain)
	it := object("logs/app.txt", "text/plain", stored)
	it.ContentEncoding = "gzip"
	return it, stored
}

// TestTranscoding is §10.7 and CT gzip (firebase.endpoints.test.ts:481-550): without
// Accept-Encoding: gzip the reply is the decompressed bytes with no Content-Encoding and no
// Content-Length, Range ignored; with it, the stored bytes with Content-Encoding: gzip.
func TestTranscoding(t *testing.T) {
	plain := []byte(strings.Repeat("hello gzip ", 1000))
	it, stored := gzipObject(t, plain)
	for _, m := range []Mode{RulesDecided, Token, GCS, Console} {
		rec, err := get(t, newGet("Range", "bytes=0-9"), it, stored, m, generous)
		if err != nil || rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), plain) {
			t.Fatalf("mode %d: %d %v %d bytes", m, rec.Code, err, rec.Body.Len())
		}
		h := rec.Header()
		if _, ok := h["Content-Length"]; ok {
			t.Errorf("Content-Length %q", h["Content-Length"])
		}
		if _, ok := h["Content-Encoding"]; ok {
			t.Errorf("Content-Encoding %q", h["Content-Encoding"])
		}
		// The stored bytes' encoding and checksums: Go sees uncompressedByServer and Node
		// !safeToValidate, so neither checks a hash against the decompressed bytes.
		if field(h, "x-goog-stored-content-encoding") != "gzip" || field(h, "Content-Range") != "" ||
			field(h, "Vary") != "Accept-Encoding" || field(h, "Accept-Ranges") != "none" {
			t.Errorf("mode %d headers %v", m, h)
		}
	}
	rec, err := get(t, newGet("Accept-Encoding", "gzip", "Range", "bytes=0-9"), it, stored, GCS, generous)
	if err != nil || rec.Code != 206 || !bytes.Equal(rec.Body.Bytes(), stored[:10]) {
		t.Fatalf("gzip accepted: %d %v", rec.Code, err)
	}
	if h := rec.Header(); field(h, "Content-Encoding") != "gzip" || field(h, "Content-Length") != "10" ||
		field(h, "Content-Range") != "bytes 0-9/"+strconv.Itoa(len(stored)) {
		t.Errorf("gzip accepted headers %v", h)
	}
}

// TestShortTranscodingHasNoContentLength: a decompressed reply short enough for net/http's
// buffer leaves the server without Content-Length too, as CT gzip wants it ("hello world",
// firebase.endpoints.test.ts:481-514 and gcs.endpoints.test.ts:405-438). The recorder above
// cannot see that: net/http adds the length on the wire. On an ephemeral port.
func TestShortTranscodingHasNoContentLength(t *testing.T) {
	plain := []byte("hello world")
	it, stored := gzipObject(t, plain)
	for _, m := range []Mode{RulesDecided, Token, GCS} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := Serve(w, r, it, bytes.NewReader(stored), m, generous); err != nil {
				t.Error(err)
			}
		}))
		tr := &http.Transport{DisableCompression: true}
		res, err := (&http.Client{Transport: tr}).Get(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		tr.CloseIdleConnections()
		srv.Close()
		if res.StatusCode != 200 || !bytes.Equal(body, plain) || res.ContentLength != -1 ||
			res.Header.Get("Content-Length") != "" || res.Header.Get("Content-Encoding") != "" {
			t.Errorf("mode %d: %d %q, length %d, headers %v", m, res.StatusCode, body, res.ContentLength, res.Header)
		}
	}
}

func TestAcceptEncoding(t *testing.T) {
	plain := []byte(strings.Repeat("x", 4096))
	it, stored := gzipObject(t, plain)
	cases := []struct {
		ae         []string
		decompress bool
	}{
		{nil, true},
		{[]string{"Accept-Encoding", "gzip"}, false},
		{[]string{"Accept-Encoding", "GZIP"}, false},
		{[]string{"Accept-Encoding", "x-gzip"}, false},
		{[]string{"Accept-Encoding", "*"}, false},
		{[]string{"Accept-Encoding", "gzip, deflate, br"}, false},
		{[]string{"Accept-Encoding", "br;q=1.0, gzip;q=0.5"}, false},
		{[]string{"Accept-Encoding", "gzip;q=0"}, true},
		{[]string{"Accept-Encoding", "gzip; q=0.000"}, true},
		{[]string{"Accept-Encoding", "*;q=0"}, true},
		// An explicit gzip;q=0 wins over * (RFC 9110 §12.5.3), in either order.
		{[]string{"Accept-Encoding", "*, gzip;q=0"}, true},
		{[]string{"Accept-Encoding", "gzip;q=0, *"}, true},
		{[]string{"Accept-Encoding", "x-gzip;q=0, *;q=1"}, true},
		{[]string{"Accept-Encoding", "gzip;q=0", "Accept-Encoding", "*"}, true},
		{[]string{"Accept-Encoding", "gzip;q=0, x-gzip"}, false},
		{[]string{"Accept-Encoding", "*;q=0, gzip"}, false},
		{[]string{"Accept-Encoding", "identity"}, true},
		{[]string{"Accept-Encoding", "deflate, br"}, true},
		{[]string{"Accept-Encoding", ""}, true},
		{[]string{"Accept-Encoding", "br", "Accept-Encoding", "gzip"}, false},
	}
	for _, c := range cases {
		rec, err := get(t, newGet(c.ae...), it, stored, Token, generous)
		if err != nil {
			t.Fatal(err)
		}
		got := !bytes.Equal(rec.Body.Bytes(), stored)
		if got != c.decompress {
			t.Errorf("%v: decompressed %v, want %v", c.ae, got, c.decompress)
		}
	}
}

// TestNoTransform: a stored Cache-Control: no-transform forbids decompressing (GCS).
func TestNoTransform(t *testing.T) {
	it, stored := gzipObject(t, []byte("abc"))
	it.CacheControl = "public, No-Transform"
	rec, err := get(t, newGet(), it, stored, GCS, generous)
	if err != nil || !bytes.Equal(rec.Body.Bytes(), stored) || field(rec.Header(), "Content-Encoding") != "gzip" {
		t.Errorf("%d %v %v", rec.Code, err, rec.Header())
	}
}

// TestNotGzip: bytes stored as gzip that are not gzip are served as stored, never a 500.
func TestNotGzip(t *testing.T) {
	data := []byte("plain text, labelled gzip")
	it := object("a.txt", "text/plain", data)
	it.ContentEncoding = "gzip"
	rec, err := get(t, newGet("Range", "bytes=0-1"), it, data, Token, generous)
	if err != nil || rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), data) ||
		field(rec.Header(), "Content-Length") != strconv.Itoa(len(data)) {
		t.Errorf("%d %v %q", rec.Code, err, rec.Body)
	}
}

// TestCorruptGzip: a stream that breaks after the headers is an error, so the caller cuts the
// connection and the client never takes a chunked reply that ended early for a whole one.
func TestCorruptGzip(t *testing.T) {
	plain := bytes.Repeat([]byte("0123456789"), 100000)
	it, stored := gzipObject(t, plain)
	bad := bytes.Clone(stored)
	for i := len(bad) / 2; i < len(bad)/2+64; i++ {
		bad[i] ^= 0xff
	}
	_, err := get(t, newGet(), it, bad, Token, generous)
	if err == nil {
		t.Fatal("no error on a corrupt stream")
	}
	truncated := stored[:len(stored)/2]
	if _, err := get(t, newGet(), it, truncated, Token, generous); err == nil {
		t.Fatal("no error on a truncated stream")
	}
}

// bomb returns gzip members that decompress to members × 16 MiB of zeros, about 16 KiB each,
// concatenated (multistream, which gzip.Reader follows by default).
func bomb(t testing.TB, members int) []byte {
	one := gzipped(t, make([]byte, 16<<20))
	return bytes.Repeat(one, members)
}

// TestGzipBombConstantMemory streams a 1 GiB decompression through Serve, with the cut at 8
// MiB and with no cut at all, and holds what Serve allocates to a few buffers: the emulator's
// gunzipSync would allocate the whole gigabyte (F §2.1).
func TestGzipBombConstantMemory(t *testing.T) {
	if testing.Short() {
		t.Skip("decompresses a gigabyte")
	}
	stored := bomb(t, 64) // 1 GiB decompressed
	it := object("bomb.txt", "text/plain", stored)
	it.ContentEncoding = "gzip"
	t.Logf("bomb: %d bytes stored, %d decompressed", len(stored), 64*16<<20)

	measure := func(lim Limits) (*discard, error, uint64) {
		w := &discard{h: http.Header{}}
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		err := serveCatch(w, newGet(), it, bytes.NewReader(stored), Token, lim)
		runtime.ReadMemStats(&after)
		return w, err, after.TotalAlloc - before.TotalAlloc
	}

	w, err, alloc := measure(Limits{DecompressedBytes: 8 << 20, MinBytesPerSecond: 1})
	if !errors.Is(err, ErrDecompressedTooLarge) {
		t.Fatalf("cut: %v", err)
	}
	if w.status != 200 || w.n > 8<<20 {
		t.Errorf("cut: status %d, %d bytes written past the 8 MiB cap", w.status, w.n)
	}
	if alloc > 4<<20 {
		t.Errorf("cut: allocated %d bytes", alloc)
	}

	w, err, alloc = measure(Limits{DecompressedBytes: 1 << 30, MinBytesPerSecond: 1})
	if err != nil || w.n != 1<<30 {
		t.Fatalf("whole: %v, %d bytes", err, w.n)
	}
	if alloc > 4<<20 {
		t.Errorf("whole: allocated %d bytes for 1 GiB", alloc)
	}
	t.Logf("allocated %d bytes for 1 GiB decompressed", alloc)

	// One byte over the cap is a cut.
	_, err, _ = measure(Limits{DecompressedBytes: 1<<30 - 1, MinBytesPerSecond: 1})
	if !errors.Is(err, ErrDecompressedTooLarge) {
		t.Errorf("cap - 1: %v", err)
	}
}

// TestIdentityConstantMemory: a large stored object streams through a buffer, never into memory.
func TestIdentityConstantMemory(t *testing.T) {
	const size = 256 << 20
	it := object("big.bin", "", nil)
	it.Size = size
	w := &discard{h: http.Header{}}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	err := serveCatch(w, newGet(), it, &zeros{size: size}, GCS, generous)
	runtime.ReadMemStats(&after)
	if err != nil || w.n != size {
		t.Fatalf("%v %d", err, w.n)
	}
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 1<<20 {
		t.Errorf("allocated %d bytes for %d", alloc, size)
	}
}
