package media

import (
	"bufio"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// Decompressive transcoding (ADR-022 §10.7): an object stored with Content-Encoding: gzip, for
// a request without Accept-Encoding: gzip and an object without no-transform, is sent
// decompressed, without Content-Encoding or Content-Length, and Range is ignored (GCS's
// transcoding; CT gzip, firebase.endpoints.test.ts:481-514). Never in memory, as the
// emulator's gunzipSync does (F §2.1): ten megabytes of zeros inflate to ten gigabytes. The
// output stops at Limits.DecompressedBytes (object_bytes), and past it the reply is cut.

// gzipHeaderOK reports whether body starts with a gzip header, and leaves it at its start.
func gzipHeaderOK(body io.ReadSeeker) (bool, error) {
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return false, fmt.Errorf("media: seek: %w", err)
	}
	_, herr := gzip.NewReader(bufio.NewReaderSize(body, 512))
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return false, fmt.Errorf("media: seek: %w", err)
	}
	return herr == nil, nil
}

// gunzip streams body's decompression to w, a buffer at a time, through every gzip member
// (multistream, as GCS and the emulator). Memory is the inflater's window and two buffers,
// whatever the ratio.
func gunzip(w http.ResponseWriter, r *http.Request, body io.Reader, limit int64, p *pacer) error {
	zr, err := gzip.NewReader(bufio.NewReaderSize(body, bufSize))
	if err != nil {
		return fmt.Errorf("media: gzip: %w", err)
	}
	buf := make([]byte, bufSize)
	var total int64
	for {
		if err := r.Context().Err(); err != nil {
			return err
		}
		n, err := zr.Read(buf)
		if n > 0 {
			total += int64(n)
			if total > limit {
				return ErrDecompressedTooLarge
			}
			if _, werr := w.Write(buf[:n]); werr != nil {
				return werr
			}
			p.wrote(n)
		}
		switch {
		case errors.Is(err, io.EOF):
			return nil
		case err != nil:
			// A corrupt stream after the headers: the reply is short, and must not end as
			// a whole one.
			return fmt.Errorf("media: gzip: %w", err)
		}
	}
}
