package gcs

import (
	"cmp"
	"errors"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/arumadev/aruma/internal/services/storage/core"
	"github.com/arumadev/aruma/internal/services/storage/wire"
)

// The uploads of the GCS surface (S4-B; ADR-022 §6.2, B §5): POST /upload/storage/v1/b/<bucket>/o
// with uploadType media, multipart or resumable, and the requests on a resumable session.
//
// route has resolved the bucket, taken the request's place under upload_requests_per_ip and,
// for the three starts, authenticated the caller as a service account of the bucket's project
// (storage.objects.create). name is the query's name
// parameter, decoded once with form rules, "" when absent (a multipart upload may carry it in
// its JSON instead, B §5.2). The handlers answer every refusal with writeError, write resources
// with writeJSON and object, and read the metadata JSON with readJSON and decodeAttrs, the
// preconditions with preconditions.
//
// Every reply that completes an object carries the crc32c and md5Hash the server computed: Node
// deletes the object it just wrote when crc32c is missing or differs (B §5.8). A checksum the
// client claims, in the JSON or in X-Goog-Hash, that is not the data's is 400 and no object.

// uploadMedia stores the body as the object's bytes: uploadType=media. The request's
// Content-Type is the object's.
func (a *API) uploadMedia(w http.ResponseWriter, r *http.Request, b core.Bucket, name string, p core.Principal) {
	if !a.allowStart(w, r) {
		return
	}
	spec, err := a.uploadSpec(r, name, nil, r.Header.Get("Content-Type"), -1)
	if err != nil {
		a.writeError(w, err, "", "")
		return
	}
	body := a.uploadBody(w, r.Body)
	o, err := a.svc.Insert(r.Context(), b, p, spec, body, r.ContentLength)
	a.inserted(w, body, o, err, b, spec.Name)
}

// uploadMultipart stores a multipart/related body, its JSON metadata then its bytes:
// uploadType=multipart (Node's resumable:false, Go under 16 MiB). The media part streams to
// the disk; only the JSON part, at most json_body_bytes, is held (ADR-022 §6.1).
func (a *API) uploadMultipart(w http.ResponseWriter, r *http.Request, b core.Bucket, name string, p core.Principal) {
	if !a.allowStart(w, r) {
		return
	}
	body := a.uploadBody(w, r.Body)
	rel, err := wire.NewRelatedReader(body, r.Header.Get("Content-Type"), a.svc.Limits().JSONBodyBytes)
	if err != nil {
		a.uploadError(w, body, err, b.Name, name)
		return
	}
	spec, err := a.uploadSpec(r, name, rel.JSON, rel.MediaType, -1)
	if err != nil {
		body.beforeReply()
		a.writeError(w, err, "", "")
		return
	}
	o, err := a.svc.Insert(r.Context(), b, p, spec, rel.Media, -1)
	a.inserted(w, body, o, err, b, spec.Name)
}

// startResumable starts a session: uploadType=resumable without an upload_id. Its reply carries
// the absolute session URL in Location (wire.GCSUploadURL from PublicURL).
//
// The metadata is the start's JSON body merged with X-Upload-Content-Type: Node moves
// contentType out of the body into the header and sends {} (B §5.3), Go sends a sniffed type
// there when the body has none. X-Upload-Content-Length, when sent, is the declared size. The
// reply's body is empty: both clients ignore it.
func (a *API) startResumable(w http.ResponseWriter, r *http.Request, b core.Bucket, name string, p core.Principal) {
	if !a.allowStart(w, r) {
		return
	}
	js, err := a.readJSON(r)
	if err != nil {
		a.writeError(w, err, "", "")
		return
	}
	declared := int64(-1)
	if v := strings.Trim(r.Header.Get(wire.XUploadContentLength), " \t"); v != "" {
		if declared, err = wire.ParseLength(v); err != nil {
			a.writeError(w, invalidf("Invalid value for %s: %q.", wire.XUploadContentLength, v), "", "")
			return
		}
	}
	spec, err := a.uploadSpec(r, name, js, r.Header.Get(wire.XUploadContentType), declared)
	if err != nil {
		a.writeError(w, err, "", "")
		return
	}
	id, err := a.svc.StartUpload(r.Context(), b, p, spec)
	if err != nil {
		a.writeError(w, err, b.Name, spec.Name)
		return
	}
	h := w.Header()
	h.Set("Location", wire.GCSUploadURL(a.opts.PublicURL, b.Name, spec.Name, id))
	h.Set("Cache-Control", "no-cache, no-store, max-age=0, must-revalidate")
	h.Set("Content-Length", "0")
	w.WriteHeader(http.StatusOK)
}

// resumeUpload serves a request on a session: any PUT, POST or DELETE with an upload_id,
// whatever its uploadType (B §5.3). The upload_id is the credential, and Authorization is
// ignored (ADR-022 §7). name is the query's name, as the session URL carries it.
//
// Content-Range says what the request is (B §10.2-5, wire.ParseContentRange): bytes */* without
// a body asks the state; bytes a-b/* is a chunk in the middle; bytes a-b/T and bytes */T end the
// object, the latter with an empty last chunk (Go, when the object ends on a chunk's boundary,
// and Node's bytes N-(N-1)/N); bytes N-*/* and bytes N-*/T carry the rest of the object and end
// it with the body, even an empty one (Node's save()). A request without Content-Range is the
// whole object in one request. A chunk that leaves the object unfinished is answered as each
// client reads it (wire.WriteIncomplete): 308 with Range for Node, 200 with
// X-Http-Status-Code-Override: 308 for Go's X-GUploader-No-308. A DELETE cancels the session:
// 499, as GCS answers it.
//
// A chunk is all or nothing for Go, which ignores Range and assumes the whole chunk was taken
// (B §5.7): core keeps what it synced and announces only that, a repeated chunk's bytes already
// kept are skipped, and a chunk that starts past them is 400 and leaves the session active.
func (a *API) resumeUpload(w http.ResponseWriter, r *http.Request, b core.Bucket, name, id string) {
	ctx := r.Context()
	if r.Method == http.MethodDelete {
		if _, err := a.svc.CancelUpload(ctx, b, name, id); err != nil {
			a.writeError(w, err, b.Name, name)
			return
		}
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(statusClientClosed)
		return
	}
	cr := wire.ContentRange{Start: 0, End: -1, Open: true, Total: -1}
	if v := r.Header.Get("Content-Range"); v != "" {
		var err error
		if cr, err = wire.ParseContentRange(strings.Trim(v, " \t")); err != nil {
			a.writeError(w, invalidf("Invalid Content-Range: %q.", v), "", "")
			return
		}
	}
	if cr.Query() {
		if r.ContentLength > 0 {
			a.writeError(w, invalidf("A status request (Content-Range: bytes */*) has no body."), "", "")
			return
		}
		st, err := a.svc.QueryUpload(ctx, b, name, id)
		a.uploadState(w, r, st, err, b.Name, name)
		return
	}
	hashes, err := wire.ParseHashHeader(r.Header.Values(wire.GoogHash))
	if err != nil {
		a.writeError(w, invalidf("Invalid %s header.", wire.GoogHash), "", "")
		return
	}
	c := core.Chunk{Offset: cr.Start, Length: cr.Length(), Finalize: cr.Final(), Total: cr.Total, Want: checksums(hashes)}
	switch {
	case cr.Start < 0:
		// bytes */T: no byte, and the object is T long, so the bytes kept must be T.
		c.Offset = cr.Total
	case cr.Open:
		c.Length = r.ContentLength
	}
	if r.ContentLength >= 0 && c.Length >= 0 && r.ContentLength != c.Length {
		a.writeError(w, invalidf("The request's Content-Length is %d, and its Content-Range %s.", r.ContentLength, cr), "", "")
		return
	}
	body := a.uploadBody(w, r.Body)
	c.Body = body
	st, err := a.svc.WriteUpload(ctx, b, name, id, nil, c)
	a.countIn(body)
	if body.err != nil && err != nil && st.Status != core.UploadFinal {
		a.uploadError(w, body, err, b.Name, name)
		return
	}
	body.beforeReply()
	a.uploadState(w, r, st, err, b.Name, name)
}

// uploadState answers a request on a session with the state it left: the object when the
// session ended with one, the refusal it ended with, or the bytes kept while it is active.
func (a *API) uploadState(w http.ResponseWriter, r *http.Request, st core.UploadState, err error, bucket, name string) {
	switch {
	case err != nil:
		a.writeError(w, err, bucket, name)
	case st.Status == core.UploadFinal && st.Object != nil:
		a.writeJSON(w, http.StatusOK, a.object(*st.Object))
	case st.Status == core.UploadActive:
		wire.WriteIncomplete(w, r, st.Received)
	default:
		a.writeError(w, core.ErrUploadCancelled, bucket, name)
	}
}

// statusClientClosed is what GCS answers a cancelled resumable upload with ("If successful, the
// response contains a 499 status code").
const statusClientClosed = 499

// uploadSpec reads what an upload's start says about the object: the JSON metadata (js, nil for
// none), the name from the query, else from the JSON (B §5.2), the content type from the JSON,
// else headerType (X-Upload-Content-Type, the media part's or the request's Content-Type), the
// preconditions, and the checksums the JSON and X-Goog-Hash claim.
func (a *API) uploadSpec(r *http.Request, name string, js []byte, headerType string, declared int64) (core.UploadSpec, error) {
	attrs, err := decodeAttrs(js)
	if err != nil {
		return core.UploadSpec{}, err
	}
	name = cmp.Or(name, attrs.Name)
	if name == "" {
		return core.UploadSpec{}, requiredf("Upload requests must include an uploadType URL parameter and a URL-encoded object name.")
	}
	q := r.URL.Query()
	pre, err := preconditions(q)
	if err != nil {
		return core.UploadSpec{}, err
	}
	hashes, err := wire.ParseHashHeader(r.Header.Values(wire.GoogHash))
	if err != nil {
		return core.UploadSpec{}, invalidf("Invalid %s header.", wire.GoogHash)
	}
	want, err := mergeClaims(attrs.Want, checksums(hashes))
	if err != nil {
		return core.UploadSpec{}, err
	}
	patch := attrs.Patch
	if ct := strings.Trim(headerType, " \t"); ct != "" && patch.Fields[core.FieldContentType] == nil {
		if patch.Fields == nil {
			patch.Fields = map[string]*string{}
		}
		patch.Fields[core.FieldContentType] = new(ct)
	}
	return core.UploadSpec{
		Name: name, Attrs: patch, Declared: declared, Surface: core.SurfaceGCS, Pre: pre, Want: want,
		IP: a.clientIP(r),
	}, nil
}

// checksums are the claims of an X-Goog-Hash header.
func checksums(h wire.Hashes) core.Checksums {
	var c core.Checksums
	if h.HasCRC32C {
		c.CRC32C = new(h.CRC32C)
	}
	if h.HasMD5 {
		c.MD5 = new(h.MD5)
	}
	return c
}

// mergeClaims joins the checksums a start's JSON and its X-Goog-Hash claim: two claims of one
// checksum that differ cannot both be the data's.
func mergeClaims(js, header core.Checksums) (core.Checksums, error) {
	out := js
	if header.CRC32C != nil {
		if js.CRC32C != nil && *js.CRC32C != *header.CRC32C {
			return core.Checksums{}, core.ErrChecksumMismatch
		}
		out.CRC32C = header.CRC32C
	}
	if header.MD5 != nil {
		if js.MD5 != nil && *js.MD5 != *header.MD5 {
			return core.Checksums{}, core.ErrChecksumMismatch
		}
		out.MD5 = header.MD5
	}
	return out, nil
}

// allowStart takes an upload start from the client's address (core.AllowUploadStart), and
// answers 429 with Retry-After when the address or the process is over its rate.
func (a *API) allowStart(w http.ResponseWriter, r *http.Request) bool {
	ok, wait := a.svc.AllowUploadStart(a.clientIP(r))
	if ok {
		return true
	}
	a.svc.Metrics().Rejections.WithLabelValues("start_rate").Inc()
	w.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(wait.Seconds())))))
	a.writeError(w, errStartRate, "", "")
	return false
}

// errStartRate is an upload start over upload_starts_per_minute_per_ip or
// upload_starts_per_second (ADR-022 §17).
var errStartRate = &apiError{status: http.StatusTooManyRequests, reason: "rateLimitExceeded",
	message: "Too many upload requests. Try again later."}

// inserted answers a one-request upload: the object, or why it is not there.
func (a *API) inserted(w http.ResponseWriter, body *requestBody, o core.Object, err error, b core.Bucket, name string) {
	a.countIn(body)
	if err != nil {
		a.uploadError(w, body, err, b.Name, name)
		return
	}
	body.beforeReply()
	a.writeJSON(w, http.StatusOK, a.object(o))
}

// uploadError answers an upload that failed. A body that could not be read is the cause
// whatever core made of it: over the entry point's cap 413, a malformed multipart body 400, a
// client too slow for min_bytes_per_second 408, any other read failure 400.
func (a *API) uploadError(w http.ResponseWriter, body *requestBody, err error, bucket, name string) {
	// The write deadline is the read's: a body cut at its window's end would leave the refusal
	// no time to reach the client.
	body.beforeReply()
	cause := body.err
	if cause == nil {
		cause = err
	}
	if _, ok := errors.AsType[*http.MaxBytesError](cause); ok {
		a.writeError(w, core.ErrTooLarge, bucket, name)
		return
	}
	switch {
	case errors.Is(cause, wire.ErrBadMultipart):
		a.writeError(w, invalidf("The multipart/related body is malformed: one JSON part, then the media."), "", "")
	case body.err != nil && errors.Is(body.err, os.ErrDeadlineExceeded):
		a.writeError(w, &apiError{status: http.StatusRequestTimeout, reason: wire.ReasonInvalid,
			message: "The upload was slower than this server allows."}, "", "")
	case body.err != nil:
		a.writeError(w, invalidf("The request body could not be read."), "", "")
	default:
		a.writeError(w, err, bucket, name)
	}
}

// countIn adds the bytes a request brought to the bytes received on this surface.
func (a *API) countIn(body *requestBody) {
	if body.n > 0 {
		a.svc.Metrics().Bytes.WithLabelValues("in", core.SurfaceGCS.String()).Add(float64(body.n))
	}
}

// The deadlines of an upload request (ADR-022 §6.4). The server's ReadTimeout and WriteTimeout
// (60 s) would cut every upload longer than a minute, and lifting them would let a client that
// sends nothing hold a connection for ever. So both move a window at a time: each window of
// uploadWindow must bring min_bytes_per_second × uploadWindow bytes, or the read blocked in it
// fails at the window's end; and the write deadline is set again just before the reply, so the
// reply of an upload that took longer than a minute still reaches its client.

// uploadWindow is a window's length; a variable for the tests.
var uploadWindow = 30 * time.Second

// requestBody is an upload's body as core reads it: it moves the deadlines as the bytes come,
// counts them, and keeps the first read error, which says why an upload failed better than what
// core made of it.
type requestBody struct {
	r     io.Reader
	rc    *http.ResponseController
	quota int64 // the bytes one window must bring; 0 when pacing is off
	got   int64 // in the current window
	n     int64 // in all
	err   error // the first error but io.EOF
}

// uploadBody starts the first window over r, or paces nothing when min_bytes_per_second is not
// positive or w has no deadlines to move (a test recorder).
func (a *API) uploadBody(w http.ResponseWriter, body io.Reader) *requestBody {
	rb := &requestBody{r: body, rc: http.NewResponseController(w)}
	if rate := a.svc.Limits().MinBytesPerSecond; rate > 0 {
		rb.quota = max(rate*int64(uploadWindow/time.Second), rate)
		if !rb.extend() {
			rb.quota = 0
		}
	}
	return rb
}

func (b *requestBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	b.n += int64(n)
	if b.quota > 0 {
		b.got += int64(n)
		if b.got >= b.quota {
			b.got = 0
			b.extend()
		}
	}
	if err != nil && err != io.EOF && b.err == nil {
		b.err = err
	}
	return n, err
}

// extend sets the read and write deadlines a window from now, and reports whether w supports
// them.
func (b *requestBody) extend() bool {
	d := time.Now().Add(uploadWindow)
	return b.rc.SetReadDeadline(d) == nil && b.rc.SetWriteDeadline(d) == nil
}

// beforeReply gives the reply a window of its own, whatever the upload took.
func (b *requestBody) beforeReply() {
	if b.quota > 0 {
		_ = b.rc.SetWriteDeadline(time.Now().Add(uploadWindow))
	}
}
