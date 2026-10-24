package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/arumadev/aruma/internal/services/storage/core"
	"github.com/arumadev/aruma/internal/services/storage/wire"
)

// The Firebase client upload protocol (A §3.8-3.13, §7; ADR-022 §6.1, §7) on core's one session
// state machine:
//
//   - A request is told by X-Goog-Upload-Protocol (multipart or resumable) or by the presence of
//     X-Goog-Upload-Command; uploadType and the order of the query are ignored (A §6.7). Without
//     either, the body is a media upload named by ?name= (E §3.1).
//   - A start answers 200 with an empty body (iOS asserts it), X-Goog-Upload-Status: active and
//     an absolute X-Goog-Upload-URL from public_url, never from the Host header (ADR-022 §6.1),
//     and never X-Goog-Upload-Chunk-Granularity (iOS rounds its offset down by it, A §6.5).
//   - A session URL needs no credential: the identity was bound at start, and Authorization on a
//     chunk, a query or a cancel counts for nothing (ADR-022 §7).
//   - X-Goog-Upload-Status: final goes with every terminal refusal, so Android does not resend
//     the chunk and iOS does not loop on query (ADR-022 §6.1). active goes with a 2xx on a live
//     session, with a gap (the client queries, then resumes from X-Goog-Upload-Size-Received),
//     and with a 503 (pressure the three SDKs retry); a session the client must forget (an
//     unknown id) and a request the server cannot read get neither.
//   - A request with a body moves its own deadlines (pacedBody): a slow upload that keeps
//     min_bytes_per_second gets its reply after any length of time, a slowloris is cut (ADR-022
//     §6.4).

// paceWindow is the window min_bytes_per_second is measured over; a variable for the tests.
var paceWindow = 30 * time.Second

// errBadCommand is an X-Goog-Upload-Command that names no request this URL takes; the emulator
// answers "bogus" 400 (raw_resumable.json).
var errBadCommand = statusError(http.StatusBadRequest, "Invalid X-Goog-Upload-Command.")

// errNoName is an upload that names no object: 400, as the emulator answers it (raw_upload.json).
var errNoName = statusError(http.StatusBadRequest, "The upload names no object.")

// upload serves a POST or PUT on /o or /o/{object} that is neither a session request, a
// metadata update nor a token call, and whose X-Goog-Upload-Protocol is not multipart: a
// resumable start (X-Goog-Upload-Protocol: resumable, or X-Goog-Upload-Command: start) or a
// media upload (A §3.10, §7). name is ?name= decoded once, else the path's object (uploadName);
// p is the request's principal, which a resumable start binds to its session (ADR-022 §7).
//
// The router has resolved the bucket, authenticated the request, set the API's CORS headers and
// taken the request's place under upload_requests_per_ip. The replies go through writeError
// (with X-Goog-Upload-Status set first where the protocol wants it) and writeObject.
func (a *API) upload(w http.ResponseWriter, r *http.Request, b core.Bucket, name string, p core.Principal) {
	protocol := strings.Trim(r.Header.Get(wire.UploadProtocol), " \t")
	command, hasCommand := r.Header[http.CanonicalHeaderKey(wire.UploadCommand)]
	switch {
	case strings.EqualFold(protocol, "resumable") || (protocol == "" && hasCommand):
		cmds, err := wire.ParseUploadCommand(strings.Join(command, ","))
		if err != nil || cmds != wire.CommandStart {
			a.writeError(w, r, errBadCommand)
			return
		}
		a.start(w, r, b, name, p)
	case protocol == "":
		a.media(w, r, b, name, p)
	default:
		a.writeError(w, r, statusError(http.StatusBadRequest, "Invalid X-Goog-Upload-Protocol."))
	}
}

// start begins a resumable upload (A §3.10): the name from ?name= or the path, the metadata
// from the JSON body (empty on Android, partial on every SDK), the content type from the body,
// then X-Goog-Upload-Header-Content-Type or X-Goog-Upload-Content-Type, the declared size from
// X-Goog-Upload-Header-Content-Length or X-Goog-Upload-Content-Length, or none (Android).
// Nothing is reserved and the rules decide at finalize (ADR-022 §6.5, §8).
func (a *API) start(w http.ResponseWriter, r *http.Request, b core.Bucket, name string, p core.Principal) {
	if name == "" {
		a.writeError(w, r, errNoName)
		return
	}
	declared, err := wire.ParseDeclaredLength(r.Header)
	if err != nil {
		a.writeError(w, r, statusError(http.StatusBadRequest, "Invalid declared upload size."))
		return
	}
	limit := a.svc.Limits().JSONBodyBytes
	js, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	switch {
	case err != nil:
		a.writeError(w, r, statusError(http.StatusBadRequest, "The request body could not be read."))
		return
	case int64(len(js)) > limit:
		a.writeError(w, r, statusError(http.StatusRequestEntityTooLarge, "The request body is too large."))
		return
	}
	res, err := parseResource(js)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	if _, ok := res.attrs.Fields[core.FieldContentType]; !ok {
		if t := wire.DeclaredType(r.Header); t != "" {
			res.setField(core.FieldContentType, t)
		}
	}
	ip := a.clientIP(r)
	if !a.allowStart(w, r, ip) {
		return
	}
	id, err := a.svc.StartUpload(r.Context(), b, p, core.UploadSpec{
		Name: name, Attrs: res.attrs, Declared: declared, Surface: core.SurfaceFirebase, Want: res.want, IP: ip,
	})
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	h := w.Header()
	wire.Set(h, wire.UploadStatus, wire.StatusActive)
	wire.Set(h, wire.UploadURL, wire.FirebaseUploadURL(a.opts.PublicURL, b.Name, name, id))
	w.WriteHeader(http.StatusOK)
}

// media serves a raw body named by ?name= or the path, with its Content-Type as the content type:
// no SDK sends one (A §3.8), the REST style does (E §3.1, raw_upload.json).
func (a *API) media(w http.ResponseWriter, r *http.Request, b core.Bucket, name string, p core.Principal) {
	if name == "" {
		a.writeError(w, r, errNoName)
		return
	}
	var res resource
	if t := strings.Trim(r.Header.Get("Content-Type"), " \t"); t != "" {
		res.setField(core.FieldContentType, t)
	}
	ip := a.clientIP(r)
	if !a.allowStart(w, r, ip) {
		return
	}
	body := a.pace(w, r)
	o, err := a.svc.Insert(r.Context(), b, p, core.UploadSpec{
		Name: name, Attrs: res.attrs, Declared: -1, Surface: core.SurfaceFirebase, IP: ip,
	}, body, r.ContentLength)
	a.received(body.n)
	body.replying()
	if err != nil {
		a.writeBodyError(w, r, body, err)
		return
	}
	writeObject(w, http.StatusOK, o)
}

// allowStart takes an upload start from the client's address (upload_starts_per_minute_per_ip,
// then the process's upload_starts_per_second), and answers 503 with a Retry-After when it is
// refused: iOS retries a 503 and not a 429 (A §5.3).
func (a *API) allowStart(w http.ResponseWriter, r *http.Request, ip netip.Addr) bool {
	ok, wait := a.svc.AllowUploadStart(ip)
	if !ok {
		a.svc.Metrics().Rejections.WithLabelValues("start_rate").Inc()
		a.writeRetry(w, r, http.StatusServiceUnavailable, wait)
	}
	return ok
}

// received counts n object bytes in from this surface.
func (a *API) received(n int64) {
	if n > 0 {
		a.svc.Metrics().Bytes.WithLabelValues("in", core.SurfaceFirebase.String()).Add(float64(n))
	}
}

// writeBodyError answers a one-request upload (media or multipart) that failed: a body cut or too
// slow is the client's, not the server's, and is said so without logging an internal error.
func (a *API) writeBodyError(w http.ResponseWriter, r *http.Request, body *pacedBody, err error) {
	switch {
	case body.slow():
		a.writeError(w, r, errTooSlow)
	case body.err != nil:
		a.writeError(w, r, errBodyCut)
	case errors.Is(err, wire.ErrBadMultipart):
		a.writeError(w, r, statusError(http.StatusBadRequest, "Invalid multipart/related body."))
	default:
		a.writeError(w, r, err)
	}
}

var (
	// errTooSlow is a body that fell below min_bytes_per_second over a window: 408, which the
	// three SDKs retry, an upload with a query first (A §5.3), so a slow client resumes from what
	// was kept while a slowloris loses its connection (ADR-022 §6.4).
	errTooSlow = statusError(http.StatusRequestTimeout, "The request body arrived too slowly.")
	// errBodyCut is a body that broke before its end.
	errBodyCut = statusError(http.StatusBadRequest, "The request body ended before its length.")
)

// uploadSession serves a POST or PUT on an upload session URL (?upload_id=): a chunk, a query,
// a cancel or a finalize, by X-Goog-Upload-Command (A §3.11-3.13). id is the session id from the
// query and name the object's (uploadName). There is no principal: Authorization on a session
// URL is ignored, since the URL itself is the credential and iOS sends none (ADR-022 §7).
//
// The router has resolved the bucket, set the API's CORS headers and taken the request's place
// under upload_requests_per_ip.
func (a *API) uploadSession(w http.ResponseWriter, r *http.Request, b core.Bucket, name, id string) {
	cmds, err := wire.ParseUploadCommand(strings.Join(r.Header.Values(wire.UploadCommand), ","))
	if err != nil {
		a.writeError(w, r, errBadCommand)
		return
	}
	ctx := r.Context()
	switch cmds {
	case wire.CommandQuery:
		st, err := a.svc.QueryUpload(ctx, b, name, id)
		if err == nil || st.Status != core.UploadCancelled {
			a.writeSession(w, r, st, err)
			return
		}
		// A cancelled session answers a query 200 with its status (E §3.1, raw_resumable.json):
		// JS and iOS fail on it, Android has stopped already.
		wire.SetUploadStatus(w.Header(), wire.StatusCancelled, st.Received)
		w.WriteHeader(http.StatusOK)
	case wire.CommandCancel:
		st, err := a.svc.CancelUpload(ctx, b, name, id)
		if err != nil {
			a.writeSession(w, r, st, err)
			return
		}
		// 200, and 200 again (CT, E §3.1); Android ignores the reply, iOS has cancelled its task.
		wire.SetUploadStatus(w.Header(), wire.StatusCancelled, st.Received)
		w.WriteHeader(http.StatusOK)
	case wire.CommandUpload, wire.CommandFinalize, wire.CommandUpload | wire.CommandFinalize:
		a.chunk(w, r, b, name, id, cmds)
	default:
		a.writeError(w, r, errBadCommand)
	}
}

// chunk serves an upload, a finalize, or both: the body is raw whatever its Content-Type (A
// §6.3), at X-Goog-Upload-Offset. A finalize alone with an empty body may leave the offset out
// (Android sends it with an empty body, A §3.11): nothing is appended then.
func (a *API) chunk(w http.ResponseWriter, r *http.Request, b core.Bucket, name, id string, cmds wire.UploadCommands) {
	offset := int64(0)
	if v := r.Header.Get(wire.UploadOffset); v != "" {
		n, err := wire.ParseLength(strings.Trim(v, " \t"))
		if err != nil {
			a.writeError(w, r, statusError(http.StatusBadRequest, "Invalid X-Goog-Upload-Offset."))
			return
		}
		offset = n
	} else if cmds != wire.CommandFinalize || r.ContentLength != 0 {
		a.writeError(w, r, statusError(http.StatusBadRequest, "X-Goog-Upload-Offset is required."))
		return
	}
	body := a.pace(w, r)
	// No Total: the declared size was bound at start, and no SDK states another on its chunks
	// that finalize checks better.
	//
	// Not the request's context: net/http cancels it the moment the body breaks (a cut
	// connection, a window short of the rate), and what arrived must still be recorded, since
	// the client resumes from it (ADR-022 §6.3). core records under its own WithoutCancel, but
	// a session's first durable byte checks the session caps with the context it was given.
	st, err := a.svc.WriteUpload(context.WithoutCancel(r.Context()), b, name, id, nil, core.Chunk{
		Offset: offset, Body: body, Length: r.ContentLength, Finalize: cmds.Has(wire.CommandFinalize), Total: -1,
	})
	a.received(body.n)
	body.replying()
	switch {
	case err == nil, st.Status == core.UploadFinal || st.Status == core.UploadCancelled:
	case body.slow():
		// What arrived is kept; the client queries and resumes.
		a.writeError(w, r, errTooSlow)
		return
	case body.err != nil:
		// A body that broke before its length is a gap from the client's side (ADR-023 §3-53).
		wire.SetUploadStatus(w.Header(), wire.StatusActive, st.Received)
		a.writeError(w, r, errBodyCut)
		return
	}
	a.writeSession(w, r, st, err)
}

// writeSession answers a request on a session with where the session stands:
//
//   - Active: 200, active and the size received, without a body (A §7).
//   - Final and succeeded: 200, final and the object's resource, on the finalize and on every
//     repeat and query, which iOS takes as the result (A §3.12, ADR-022 §6.1).
//   - Final and refused, or cancelled: the refusal's status with final, on the finalize and on
//     every repeat and query, so the client stops whatever request carried it.
//   - A gap: 400, active and the size received (ADR-022 §6.1); a body shorter than its length
//     is one too (ADR-023 §3-53).
//   - 503: active, with the size received when it is known.
//   - Anything else (an unknown session, a malformed request): the error alone.
func (a *API) writeSession(w http.ResponseWriter, r *http.Request, st core.UploadState, err error) {
	h := w.Header()
	if err == nil {
		switch {
		case st.Status == core.UploadFinal && st.Object != nil:
			wire.SetUploadStatus(h, wire.StatusFinal, st.Received)
			writeObject(w, http.StatusOK, *st.Object)
		case st.Status == core.UploadCancelled:
			wire.SetUploadStatus(h, wire.StatusCancelled, st.Received)
			w.WriteHeader(http.StatusOK)
		default:
			wire.SetUploadStatus(h, wire.StatusActive, st.Received)
			w.WriteHeader(http.StatusOK)
		}
		return
	}
	if gap, ok := errors.AsType[*core.OffsetError](err); ok {
		wire.SetUploadStatus(h, wire.StatusActive, gap.Received)
		a.writeError(w, r, err)
		return
	}
	_, invalid := errors.AsType[*core.InvalidError](err)
	switch {
	case st.Status == core.UploadFinal || st.Status == core.UploadCancelled:
		wire.SetUploadStatus(h, wire.StatusFinal, st.Received)
	case errors.Is(err, core.ErrUnavailable) || errors.Is(err, core.ErrOverloaded):
		received := int64(-1)
		if st.Status == core.UploadActive {
			received = st.Received
		}
		wire.SetUploadStatus(h, wire.StatusActive, received)
	case invalid && st.Status == core.UploadActive:
		// core refuses a chunk on a live session as malformed only when its body ends before
		// its length: a gap the client mends with a query.
		wire.SetUploadStatus(h, wire.StatusActive, st.Received)
	}
	a.writeError(w, r, err)
}

// resource is what an upload's JSON says about its object: a start's body or a multipart's
// first part.
type resource struct {
	attrs core.Patch
	name  string         // the JSON's own name, "" when it has none
	want  core.Checksums // md5Hash, when the client set one (ADR-022 §6.1)
}

func (res *resource) setField(name, v string) {
	if res.attrs.Fields == nil {
		res.attrs.Fields = map[string]*string{}
	}
	res.attrs.Fields[name] = &v
}

// parseResource reads an upload's metadata JSON for a new object (A §3.9, §3.10, §7):
//
//   - Empty is no metadata (Android's start may carry no body).
//   - The five content fields: a string sets the field; null leaves it unset.
//   - metadata: an object of strings, a null value leaving its key out; metadata: null is none
//     (iOS may send it, A §8.1). firebaseStorageDownloadTokens is dropped from it before the
//     rules and the store see it: the server alone mints tokens (ADR-022 §9).
//   - md5Hash: base64 of 16 bytes, checked against the bytes at finalize: 400 and no object
//     when it differs (ADR-023 §3-20). null is none.
//   - name: kept for a multipart that names its object nowhere else (ADR-023 §3-26).
//   - Everything else is ignored, null or not: bucket (iOS sends ""), size, crc32c, generation,
//     downloadTokens, fullPath (JS) and the rest (A §7).
//
// A field above with a value of another type is 400, as is a body that is not one JSON object.
func parseResource(js []byte) (resource, error) {
	var res resource
	if len(bytes.TrimSpace(js)) == 0 {
		return res, nil
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(js, &doc); err != nil || doc == nil {
		return res, statusError(http.StatusBadRequest, "The metadata must be a JSON object.")
	}
	for _, field := range patchFields {
		raw, ok := doc[field]
		if !ok {
			continue
		}
		v, err := nullableString(raw)
		if err != nil {
			return res, statusError(http.StatusBadRequest, "The "+field+" must be a string or null.")
		}
		if v != nil {
			res.setField(field, *v)
		}
	}
	if raw, ok := doc["metadata"]; ok && !isNull(raw) {
		var md map[string]json.RawMessage
		if err := json.Unmarshal(raw, &md); err != nil || md == nil {
			return res, statusError(http.StatusBadRequest, "The metadata must be an object or null.")
		}
		for k, raw := range md {
			v, err := nullableString(raw)
			if err != nil {
				return res, statusError(http.StatusBadRequest, "A custom metadata value must be a string or null.")
			}
			if v == nil {
				continue
			}
			if res.attrs.Metadata == nil {
				res.attrs.Metadata = map[string]*string{}
			}
			res.attrs.Metadata[k] = v
		}
		core.StripClientTokens(res.attrs.Metadata)
	}
	if raw, ok := doc["md5Hash"]; ok {
		v, err := nullableString(raw)
		if err != nil {
			return res, statusError(http.StatusBadRequest, "The md5Hash must be a string or null.")
		}
		if v != nil {
			sum, err := wire.ParseMD5(*v)
			if err != nil {
				return res, statusError(http.StatusBadRequest, "The md5Hash is not the base64 of an MD5 hash.")
			}
			res.want.MD5 = &sum
		}
	}
	if raw, ok := doc["name"]; ok {
		v, err := nullableString(raw)
		if err != nil {
			return res, statusError(http.StatusBadRequest, "The name must be a string or null.")
		}
		if v != nil {
			res.name = *v
		}
	}
	return res, nil
}

// pacedBody is a request body that moves the request's deadlines as it arrives (ADR-022 §6.4,
// F §3.5). The server's ReadTimeout and WriteTimeout (60 s, server.go) would cut every upload
// longer than a minute, and its reply with it after the commit; lifting them, as the WebSocket
// does, would let a slowloris hold a connection for ever. So both deadlines move together a
// window at a time: each window of paceWindow must bring min_bytes_per_second × paceWindow
// bytes, or the read blocked in it fails at the window's end; and the write deadline is set a
// window ahead once more just before the reply (replying), since the commit after the last byte
// may have used most of the last window. media.pacer is the same rule for downloads.
type pacedBody struct {
	r     io.Reader
	rc    *http.ResponseController
	quota int64 // the bytes one window must bring; 0 when pacing is off
	got   int64 // in the current window
	n     int64 // in all
	err   error // the body's own error, other than io.EOF
}

// pace wraps the request's body, starting its first window, or not pacing when w has no
// deadline to move (the server's own then stand).
func (a *API) pace(w http.ResponseWriter, r *http.Request) *pacedBody {
	b := &pacedBody{r: r.Body, rc: http.NewResponseController(w)}
	if rate := a.svc.Limits().MinBytesPerSecond; rate > 0 {
		b.quota = max(rate*int64(paceWindow/time.Second), rate)
		if !b.extend() {
			b.quota = 0
		}
	}
	return b
}

func (b *pacedBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	b.n += int64(n)
	if b.quota > 0 {
		if b.got += int64(n); b.got >= b.quota {
			b.got = 0
			b.extend()
		}
	}
	if err != nil && !errors.Is(err, io.EOF) && b.err == nil {
		b.err = err
	}
	return n, err
}

// extend sets both deadlines a window from now, and reports whether w supports them.
func (b *pacedBody) extend() bool {
	d := time.Now().Add(paceWindow)
	return b.rc.SetReadDeadline(d) == nil && b.rc.SetWriteDeadline(d) == nil
}

// replying gives the reply its own window: a request longer than the server's WriteTimeout gets
// its reply once the server has committed (ADR-022 §6.4: extending the read alone loses it).
func (b *pacedBody) replying() {
	if b.quota > 0 {
		_ = b.rc.SetWriteDeadline(time.Now().Add(paceWindow))
	}
}

// slow reports whether the body was cut at a window's end: below min_bytes_per_second.
func (b *pacedBody) slow() bool { return errors.Is(b.err, os.ErrDeadlineExceeded) }
