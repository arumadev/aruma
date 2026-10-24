package media

import (
	"bytes"
	"crypto/md5"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/arumadev/aruma/internal/services/storage/wire"
)

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// object returns an Item for data as stored, with its checksums and content ETag.
func object(name, contentType string, data []byte) Item {
	return Item{
		Name:           name,
		ContentType:    contentType,
		Size:           int64(len(data)),
		ETag:           wire.MediaETag(1784647629568292),
		Updated:        time.Date(2026, 10, 24, 9, 30, 15, 123456000, time.UTC),
		Generation:     1784647629568292,
		Metageneration: 3,
		CRC32C:         crc32.Checksum(data, castagnoli),
		MD5:            md5.Sum(data),
	}
}

// generous are limits no test reaches unless it means to.
var generous = Limits{DecompressedBytes: 1 << 40, MinBytesPerSecond: 4 << 10}

// get serves it from data to req and returns the recorded reply and Serve's error.
func get(t *testing.T, req *http.Request, it Item, data []byte, m Mode, lim Limits) (*httptest.ResponseRecorder, error) {
	t.Helper()
	rec := httptest.NewRecorder()
	// The server's general headers, which a media reply overrides and an error keeps.
	rec.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	rec.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
	err := Serve(rec, req, it, bytes.NewReader(data), m, lim)
	return rec, err
}

func newGet(headers ...string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/v0/b/p.aruma.test/o/x?alt=media", nil)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Add(headers[i], headers[i+1])
	}
	return req
}

// field reads a header under the exact name Serve wrote it (wire.Set keeps x-goog-* lower
// case) or under its canonical form.
func field(h http.Header, k string) string {
	if v := h[k]; len(v) > 0 {
		return v[0]
	}
	return h.Get(k)
}

// discard is a ResponseWriter that keeps nothing of the body, for the memory tests.
type discard struct {
	h      http.Header
	status int
	n      int64
}

func (d *discard) Header() http.Header         { return d.h }
func (d *discard) WriteHeader(status int)      { d.status = status }
func (d *discard) Write(b []byte) (int, error) { d.n += int64(len(b)); return len(b), nil }

// zeros is an io.ReadSeeker of size zero bytes that holds none of them.
type zeros struct{ size, off int64 }

func (z *zeros) Read(b []byte) (int, error) {
	if z.off >= z.size {
		return 0, io.EOF
	}
	n := int(min(int64(len(b)), z.size-z.off))
	clear(b[:n])
	z.off += int64(n)
	return n, nil
}

func (z *zeros) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		z.off = off
	case io.SeekCurrent:
		z.off += off
	case io.SeekEnd:
		z.off = z.size + off
	}
	return z.off, nil
}
