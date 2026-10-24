package media

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/arumadev/aruma/internal/services/storage/wire"
)

// ErrDecompressedTooLarge is a gzip object that decompressed past Limits.DecompressedBytes: the
// reply was cut (§10.7), and core.Metrics.DecompressionsCut counts it.
var ErrDecompressedTooLarge = errors.New("media: decompressed object larger than object_bytes")

// ErrShortBody is a body that ended before the object's size.
var ErrShortBody = errors.New("media: object body shorter than its size")

// bufSize is the one buffer a reply copies through.
const bufSize = 32 << 10

func serve(w http.ResponseWriter, r *http.Request, it Item, body io.ReadSeeker, m Mode, lim Limits) error {
	if refused(r.Header) {
		writeError(w, m, http.StatusForbidden)
		return nil
	}
	// The media headers go to w only with the status line, so an error returned before it
	// leaves the caller w's headers as they were.
	mh := http.Header{}
	setHeaders(mh, it, m)
	head := r.Method == http.MethodHead

	// The preconditions before Range (RFC 9110 §13.2.2): a cached copy is a 304 whatever the
	// range asks for.
	if notModified(r, it) {
		// As net/http's writeNotModified: no representation headers on a 304.
		for _, k := range []string{"Content-Type", "Content-Length", "Content-Encoding", "Content-Disposition"} {
			mh.Del(k)
		}
		send(w, mh, http.StatusNotModified)
		return nil
	}

	transcode := isGzip(it.ContentEncoding) && !acceptsGzip(r.Header) && !noTransform(it.CacheControl) &&
		lim.DecompressedBytes > 0

	// Range on the stored bytes; ignored when decompressing, as GCS ignores it (§10.7).
	var rng wire.Range
	partial := false
	if !transcode && ifRangeHolds(r.Header, it.ETag) {
		var err error
		// Several Range headers are several ranges: the whole object (§10.3).
		rng, partial, err = wire.ParseRange(strings.Join(r.Header.Values("Range"), ","), it.Size)
		if errors.Is(err, wire.ErrUnsatisfiable) {
			w.Header().Set("Content-Range", wire.UnsatisfiedContentRange(it.Size))
			writeError(w, m, http.StatusRequestedRangeNotSatisfiable)
			return nil
		}
	}

	p := newPacer(w, lim.MinBytesPerSecond)
	if transcode {
		ok, err := gzipHeaderOK(body)
		if err != nil {
			return err
		}
		if ok {
			mh.Set("Accept-Ranges", "none")
			send(w, mh, http.StatusOK)
			if !head {
				cut(lim, gunzip(w, r, body, lim.DecompressedBytes, p))
			}
			return nil
		}
		// Bytes stored as gzip that are not gzip: served whole as stored, with their
		// encoding, so the reply is what the client stored rather than a 500.
	}

	if !identity(it.ContentEncoding) {
		mh.Set("Content-Encoding", storedEncoding(it.ContentEncoding))
	}
	mh.Set("Accept-Ranges", "bytes")
	start, length := int64(0), it.Size
	status := http.StatusOK
	if partial {
		start, length = rng.Start, rng.Length
		status = http.StatusPartialContent
		mh.Set("Content-Range", rng.ContentRange(it.Size))
	}
	mh.Set("Content-Length", wire.Int(length))
	if _, err := body.Seek(start, io.SeekStart); err != nil {
		return fmt.Errorf("media: seek: %w", err)
	}
	send(w, mh, status)
	if !head && length > 0 {
		cut(lim, copyN(w, r, body, length, p))
	}
	return nil
}

// send writes the media headers over w's, and the status line.
func send(w http.ResponseWriter, mh http.Header, status int) {
	h := w.Header()
	for k, v := range mh {
		h[k] = v
	}
	w.WriteHeader(status)
}

// cut ends a reply that failed after its headers: it tells lim.Cut why, then aborts the
// handler, which cuts the connection (HTTP/1) or resets the stream (HTTP/2). A decompressed
// reply has no Content-Length, so returning would close a chunked body as if it were whole.
func cut(lim Limits, err error) {
	if err == nil {
		return
	}
	if lim.Cut != nil {
		lim.Cut(err)
	}
	panic(http.ErrAbortHandler)
}

// copyN writes length bytes of body, a buffer at a time, extending the write deadline as the
// client keeps up.
func copyN(w http.ResponseWriter, r *http.Request, body io.Reader, length int64, p *pacer) error {
	buf := make([]byte, bufSize)
	for length > 0 {
		if err := r.Context().Err(); err != nil {
			return err
		}
		n, err := body.Read(buf[:min(int64(len(buf)), length)])
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return werr
			}
			length -= int64(n)
			p.wrote(n)
		}
		switch {
		case length == 0:
			return nil
		case errors.Is(err, io.EOF):
			return ErrShortBody
		case err != nil:
			return err
		}
	}
	return nil
}

// refused reports a request a media route answers 403 (§10.1), whatever the object:
//   - a service worker's script (Service-Worker: script) or any worker's load: an uploaded JS
//     file registered as a service worker on the dashboard's origin would intercept every
//     download of the bucket and outlive the XSS that registered it (F §2.2-3);
//   - a script, style, worklet or xslt load from the dashboard's own origin (lead's decision):
//     the dashboard's CSP has script-src 'self', a JS upload passes nosniff, and the reply's
//     sandbox does not reach a subresource, so any HTML injection in the dashboard would run a
//     whole script on its origin.
//
// Loads from apps (same-site, cross-site), navigation, and <img>/<video>/<audio> from the
// dashboard itself are untouched. A browser that sends no Fetch Metadata is not covered by
// the second rule; S5-C's narrower dashboard CSP is meant for it.
func refused(h http.Header) bool {
	if _, ok := h["Service-Worker"]; ok {
		return true
	}
	dest := strings.ToLower(strings.TrimSpace(h.Get("Sec-Fetch-Dest")))
	if workerDests[dest] {
		return true
	}
	site := strings.ToLower(strings.TrimSpace(h.Get("Sec-Fetch-Site")))
	return site == "same-origin" && sameOriginDests[dest]
}

// workerDests are refused from every site.
var workerDests = map[string]bool{"worker": true, "sharedworker": true, "serviceworker": true}

// sameOriginDests are refused from the dashboard's own origin: Fetch's script-like
// destinations, style and xslt.
var sameOriginDests = map[string]bool{
	"script": true, "style": true, "worker": true, "sharedworker": true, "serviceworker": true,
	"audioworklet": true, "paintworklet": true, "xslt": true,
}

// ifRangeHolds reports whether a Range is to be honoured: no If-Range, or one equal to the
// content's ETag (§12). A date, a weak tag or another tag sends the whole object (§10.3), as
// RFC 9110 §13.1.5 allows; the ETag follows the generation alone, so a metadata change never
// restarts a resumed download.
func ifRangeHolds(h http.Header, etag string) bool {
	v := strings.TrimSpace(h.Get("If-Range"))
	if v == "" {
		return true
	}
	return etag != "" && !strings.HasPrefix(etag, "W/") && v == etag
}

// notModified reports a GET or HEAD whose validators match the object: If-None-Match by weak
// comparison, else If-Modified-Since (RFC 9110 §13.2.2).
func notModified(r *http.Request, it Item) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	if inm := r.Header.Get("If-None-Match"); inm != "" {
		if it.ETag == "" {
			return false
		}
		want := strings.TrimPrefix(it.ETag, "W/")
		for t := range strings.SplitSeq(inm, ",") {
			t = strings.TrimSpace(t)
			if t == "*" || strings.TrimPrefix(t, "W/") == want {
				return true
			}
		}
		return false
	}
	if ims := r.Header.Get("If-Modified-Since"); ims != "" {
		return !modifiedSince(it.Updated, ims)
	}
	return false
}

// acceptsGzip reports an Accept-Encoding that takes gzip (RFC 9110 §12.5.3): an explicit gzip
// or x-gzip with a q above 0, or, with neither named, a * with a q above 0. An explicit
// gzip;q=0 refuses gzip whatever * says.
func acceptsGzip(h http.Header) bool {
	named, namedOK, star, starOK := false, false, false, false
	for _, v := range h.Values("Accept-Encoding") {
		for c := range strings.SplitSeq(v, ",") {
			coding, params, _ := strings.Cut(c, ";")
			ok := !zeroQ(params)
			switch strings.ToLower(strings.TrimSpace(coding)) {
			case "gzip", "x-gzip":
				named, namedOK = true, namedOK || ok
			case "*":
				star, starOK = true, starOK || ok
			}
		}
	}
	if named {
		return namedOK
	}
	return star && starOK
}

// zeroQ reports a q=0 (q=0, q=0.0, q=0.000) among an Accept-Encoding entry's parameters.
func zeroQ(params string) bool {
	for p := range strings.SplitSeq(params, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(k), "q") {
			continue
		}
		v = strings.TrimSpace(v)
		return strings.Trim(v, "0.") == "" && strings.HasPrefix(v, "0")
	}
	return false
}

// writeError answers a refused or unsatisfiable request in its surface's error shape (§12). The
// reply is JSON, so it keeps the server's general headers (CORP same-origin, CSP
// default-src 'none'), and none of a media reply's.
func writeError(w http.ResponseWriter, m Mode, status int) {
	var msg, reason, rpc string
	switch status {
	case http.StatusForbidden:
		msg, reason, rpc = "Permission denied.", "forbidden", "PERMISSION_DENIED"
	default:
		msg, reason, rpc = "The requested range cannot be satisfied.", "requestedRangeNotSatisfiable", "OUT_OF_RANGE"
	}
	var v any
	switch m {
	case GCS:
		v = wire.GCSError{Error: wire.GCSErrorBody{Code: status, Message: msg,
			Errors: []wire.GCSErrorItem{{Domain: "global", Reason: reason, Message: msg}}}}
	case Console:
		// The dashboard's API shape, google.rpc.Status (httpx.WriteError).
		v = map[string]any{"error": map[string]any{"code": status, "message": msg, "status": rpc}}
	default:
		v = wire.FirebaseError{Error: wire.FirebaseErrorBody{Code: status, Message: msg}}
	}
	b, _ := json.Marshal(v)
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=UTF-8")
	h.Set("Cache-Control", defaultCacheControl)
	h.Set("Content-Length", wire.Int(int64(len(b))))
	w.WriteHeader(status)
	_, _ = w.Write(b)
}
