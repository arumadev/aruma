package gcs

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/arumadev/aruma/internal/services/storage/core"
	"github.com/arumadev/aruma/internal/services/storage/media"
	"github.com/arumadev/aruma/internal/services/storage/wire"
)

// download serves an object's bytes (S4-C; ADR-022 §10.5, §14, B §6.1): GET or HEAD with
// alt=media on /storage/v1/b/<bucket>/o/<object>, and on /download/storage/v1/..., the
// mediaLink. route has resolved the bucket and authenticated the caller as a service
// account of its project (storage.objects.get); name is the object's name, decoded once from
// the escaped path. It reads ?generation= with generation and the preconditions with
// preconditions, and answers every refusal before the bytes with writeError.
//
// The bytes go through media.Serve in its GCS mode, with every x-goog-* header: x-goog-hash as
// crc32c=…,md5=… in one header, which Node checks its download against by default whenever
// x-goog-stored-content-encoding is identity (B §6.1); x-goog-generation, which Go pins its
// reads to with ?generation= after a retry (B §6.2); x-goog-metageneration and
// x-goog-stored-content-length. Cache-Control is the stored value as it is, since Go reads
// ReaderObjectAttrs.CacheControl from it and it must agree with Attrs().CacheControl (ADR-022
// §10.5). The surface serves the current generation alone: another is 404 (ADR-023 §3-10).
func (a *API) download(w http.ResponseWriter, r *http.Request, b core.Bucket, name string, p core.Principal) {
	q := r.URL.Query()
	gen, err := generation(q)
	if err != nil {
		a.writeError(w, err, "", "")
		return
	}
	pre, err := preconditions(q)
	if err != nil {
		a.writeError(w, err, "", "")
		return
	}
	o, body, err := a.svc.Open(r.Context(), b, name, p, "", gen)
	if err != nil {
		a.writeError(w, err, b.Name, name)
		return
	}
	defer body.Close()
	if !holds(o, pre) {
		a.writeError(w, core.ErrPreconditionFailed, b.Name, name)
		return
	}
	lim := a.svc.Limits()
	cw := &countingWriter{ResponseWriter: w}
	err = media.Serve(cw, r, item(o), body, media.GCS, media.Limits{
		DecompressedBytes: lim.ObjectBytes,
		MinBytesPerSecond: lim.MinBytesPerSecond,
		Cut: func(err error) {
			a.countOut(cw)
			if errors.Is(err, media.ErrDecompressedTooLarge) {
				a.svc.Metrics().DecompressionsCut.Inc()
			}
		},
	})
	if err != nil {
		// Before the status line: the object's file would not seek.
		a.writeError(w, fmt.Errorf("%w: serve %q: %w", core.ErrUnavailable, name, err), b.Name, name)
		return
	}
	a.countOut(cw)
}

// item is what media.Serve needs of an object. The ETag follows the generation alone, so a
// metadata change never restarts a download in progress (ADR-022 §12).
func item(o core.Object) media.Item {
	return media.Item{
		Name: o.Name, ContentType: o.ContentType, ContentEncoding: o.ContentEncoding,
		ContentDisposition: o.ContentDisposition, ContentLanguage: o.ContentLanguage, CacheControl: o.CacheControl,
		Size: o.Size, ETag: wire.MediaETag(o.Generation), Updated: o.Updated,
		Generation: o.Generation, Metageneration: o.Metageneration, CRC32C: o.CRC32C, MD5: o.MD5,
	}
}

// countOut adds the bytes a reply carried to the bytes served on this surface.
func (a *API) countOut(cw *countingWriter) {
	if cw.n > 0 {
		a.svc.Metrics().Bytes.WithLabelValues("out", core.SurfaceGCS.String()).Add(float64(cw.n))
		cw.n = 0
	}
}

// countingWriter counts the body bytes a reply writes. Unwrap lets media.Serve move the
// connection's deadlines through it (http.ResponseController).
type countingWriter struct {
	http.ResponseWriter
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.ResponseWriter.Write(p)
	c.n += int64(n)
	return n, err
}

func (c *countingWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }
