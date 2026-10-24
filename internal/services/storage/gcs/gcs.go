// Package gcs serves Cloud Storage to the Admin SDKs over the GCS JSON API: /storage/v1,
// /upload/storage/v1 and /download/storage/v1 (ADR-022 §1, §14). Every request needs a service
// account's Bearer token, bound to its project: no emulator mode, no trusted network. A request
// on an upload session is the exception: its upload_id is the credential (ADR-022 §7).
//
// The router works on URL.EscapedPath, split before decoding (wire.SplitObjectPath), so an
// escaped "/" stays inside an object's name, and reads the query with form rules ("+" is a
// space, B §2.3). An empty parameter is an absent one, as Go sends delimiter=, prefix= and the
// like on every list (B §4.2), and the parameters that change nothing here (alt=json,
// prettyPrint, projection, fields, userProject, and every one this surface does not know) are
// ignored (B §10.2-1). What v0.3 defers answers 501 in GCS's shape (ADR-023 §2).
package gcs

import (
	"cmp"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/platform/httpx"
	"github.com/arumadev/aruma/internal/platform/ratelimit"
	"github.com/arumadev/aruma/internal/services/storage/core"
	"github.com/arumadev/aruma/internal/services/storage/wire"
)

// Options configure the API.
type Options struct {
	// PublicURL is the server's address without a trailing slash: the session Location and
	// the resources' links are absolute from it.
	PublicURL string
	// TrustedProxies are the proxies whose X-Forwarded-For is believed, for the limits per IP:
	// the server's trusted_proxies, as /v0 has them.
	TrustedProxies []netip.Prefix
	// Credentials verifies the Bearer access tokens of service accounts.
	Credentials authn.Verifier
	// Writes counts the write requests in flight per client address under
	// upload_requests_per_ip (ADR-022 §17). It is one budget per address, which /v0 shares
	// (app.go gives both the same); nil gives this API a count of its own.
	Writes *ratelimit.InFlight
}

// API is the GCS JSON endpoint.
type API struct {
	svc  *core.Service
	opts Options
	log  *slog.Logger
	// writes counts the write requests in flight per client address (upload_requests_per_ip,
	// ADR-022 §17): opts.Writes, or a count of its own.
	writes *ratelimit.InFlight
	// warnedForwarded is set once an X-Forwarded-For from an untrusted peer was logged.
	warnedForwarded atomic.Bool
}

// New returns the API.
func New(svc *core.Service, opts Options, log *slog.Logger) *API {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	opts.PublicURL = strings.TrimSuffix(opts.PublicURL, "/")
	return &API{svc: svc, opts: opts, log: log, writes: cmp.Or(opts.Writes, new(ratelimit.InFlight))}
}

// Handler returns the handler of every request under the three GCS prefixes.
//
// It routes by hand, not with ServeMux patterns: a mux cleans the path (a//b, a/./b) and
// redirects, which would change an object's name or send a client elsewhere.
func (a *API) Handler() http.Handler { return http.HandlerFunc(a.route) }

// The IAM permission each operation needs, as GCS names it in its refusals.
const (
	permBucketsGet    = "storage.buckets.get"
	permBucketsUpdate = "storage.buckets.update"
	permObjectsGet    = "storage.objects.get"
	permObjectsList   = "storage.objects.list"
	permObjectsUpdate = "storage.objects.update"
	permObjectsDelete = "storage.objects.delete"
	permObjectsCreate = "storage.objects.create"
)

// route is what a request asks for, once its path, method and query are read.
type route uint8

const (
	routeNone route = iota
	routeGetBucket
	routePatchBucket
	routeList
	routeGetObject
	routePatchObject
	routeDeleteObject
	routeRewrite
	routeDownload
	routeUploadMedia
	routeUploadMultipart
	routeStartResumable
	routeSession
)

var routeNames = [...]string{"none", "buckets.get", "buckets.patch", "objects.list", "objects.get", "objects.patch",
	"objects.delete", "objects.rewrite", "download", "upload.media", "upload.multipart", "upload.resumable", "session"}

func (r route) String() string { return routeNames[r] }

// perm is the permission a route needs; a session needs none, its upload_id is the credential.
func (r route) perm() string {
	switch r {
	case routeGetBucket:
		return permBucketsGet
	case routePatchBucket:
		return permBucketsUpdate
	case routeList:
		return permObjectsList
	case routeGetObject, routeDownload, routeRewrite:
		return permObjectsGet
	case routePatchObject:
		return permObjectsUpdate
	case routeDeleteObject:
		return permObjectsDelete
	case routeUploadMedia, routeUploadMultipart, routeStartResumable:
		return permObjectsCreate
	}
	return ""
}

// writes reports whether a route writes, so it counts against upload_requests_per_ip.
func (r route) writes() bool {
	switch r {
	case routePatchBucket, routePatchObject, routeDeleteObject, routeRewrite, routeUploadMedia, routeUploadMultipart,
		routeStartResumable, routeSession:
		return true
	}
	return false
}

// target is a classified request: its route, the bucket and object it names (the object decoded
// once from the escaped path, or for an upload the query's name), a rewrite's escaped
// destination after rewriteTo/, and a session's upload_id. A request that names no route has
// err instead: 404 for what the JSON API does not have, 501 for what v0.3 defers, 400 for a
// malformed path or query.
type target struct {
	route          route
	bucket, object string
	dest, uploadID string
	err            error
}

// deferredBucketParts are the bucket subresources of the GCS JSON API that v0.3 does not serve
// (ADR-023 §2): 501, not 404, so a caller knows the API is right and the server is not there yet.
var deferredBucketParts = map[string]bool{
	"acl": true, "defaultObjectAcl": true, "iam": true, "notificationConfigs": true,
	"lockRetentionPolicy": true, "storageLayout": true, "anywhereCaches": true, "managedFolders": true,
	"folders": true, "operations": true, "restore": true, "relocate": true,
}

// deferredObjectParts are the object subresources and actions v0.3 does not serve: compose,
// ACLs, moveTo, restore, copyTo (the SDKs copy with rewriteTo, B §4.5).
var deferredObjectParts = map[string]bool{
	"compose": true, "acl": true, "moveTo": true, "restore": true, "copyTo": true, "bulkRestore": true,
}

// classify finds a request's route from its escaped path, method, query and headers alone,
// before anything is looked up.
func classify(r *http.Request) target {
	escaped := r.URL.EscapedPath()
	if rest, ok := strings.CutPrefix(escaped, wire.GCSUploadPrefix); ok {
		return classifyUpload(r, rest)
	}
	if rest, ok := strings.CutPrefix(escaped, wire.GCSDownloadPrefix); ok {
		return classifyJSON(r, rest, true)
	}
	if rest, ok := strings.CutPrefix(escaped, wire.GCSPrefix); ok {
		return classifyJSON(r, rest, false)
	}
	return target{err: errNotFoundPath}
}

// classifyJSON classifies the JSON API under /storage/v1/, and under /download/storage/v1/,
// which serves an object's metadata and media alone (its mediaLink, B §10.1).
func classifyJSON(r *http.Request, rest string, download bool) target {
	q := r.URL.Query()
	alt := q.Get("alt")
	if alt != "" && alt != "json" && alt != "media" {
		return target{err: invalidf("Invalid alt parameter: %q.", alt)}
	}
	if rest == "b" || rest == "b/" || strings.HasPrefix(rest, "projects/") {
		// buckets.list and insert, HMAC keys, the project's service account.
		return target{err: errDeferred}
	}
	path, err := wire.SplitObjectPath(rest)
	if err != nil {
		if strings.HasPrefix(rest, "b/") {
			return target{err: invalidf("The request path is malformed.")}
		}
		return target{err: errNotFoundPath}
	}
	read := r.Method == http.MethodGet || r.Method == http.MethodHead
	media := alt == "media"
	object := path.Objects && path.Object != "" && path.Rest == ""
	switch {
	case download && !object, download && media && !read:
		return target{err: errNotFoundPath}
	case media && !(object && read):
		return target{err: invalidf("alt=media is only for reading an object.")}
	case hasEncryptionHeaders(r.Header):
		return target{err: errDeferred} // CSEK (ADR-023 §2)
	}
	t := target{bucket: path.Bucket, object: path.Object}
	switch {
	case !path.Objects && path.Rest == "":
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			t.route = routeGetBucket
		case http.MethodPatch:
			t.route = routePatchBucket
		case http.MethodPut, http.MethodDelete:
			t.err = errDeferred // buckets.update, buckets.delete
		default:
			t.err = errNotFoundPath
		}
	case !path.Objects:
		part, _, _ := strings.Cut(path.Rest, "/")
		if deferredBucketParts[part] {
			t.err = errDeferred
		} else {
			t.err = errNotFoundPath
		}
	case path.Object == "" && path.Rest == "":
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			t.route = routeList
		case http.MethodPost:
			t.err = errDeferred // objects.insert without a body: uploads go to /upload
		default:
			t.err = errNotFoundPath
		}
	case path.Rest == "":
		switch {
		case read && media:
			t.route = routeDownload
		case read:
			t.route = routeGetObject
		case download:
			t.err = errNotFoundPath
		case r.Method == http.MethodPatch:
			t.route = routePatchObject
		case r.Method == http.MethodDelete:
			t.route = routeDeleteObject
		case r.Method == http.MethodPut:
			t.err = errDeferred // objects.update: the SDKs PATCH
		default:
			t.err = errNotFoundPath
		}
	default:
		part, dest, _ := strings.Cut(path.Rest, "/")
		switch {
		case part == "rewriteTo" && r.Method == http.MethodPost:
			t.route, t.dest = routeRewrite, dest
		case deferredObjectParts[part]:
			t.err = errDeferred
		default:
			t.err = errNotFoundPath
		}
	}
	return t
}

// classifyUpload classifies /upload/storage/v1/b/<bucket>/o. An upload_id makes the request one
// on an existing session whatever its method and uploadType, since Go sends its chunks as POST
// to a URL that carries uploadType=resumable (B §5.3, ADR-022 §6.2). Without one, uploadType
// picks the protocol, and without it the upload is media (or multipart by its header).
func classifyUpload(r *http.Request, rest string) target {
	path, err := wire.SplitObjectPath(rest)
	switch {
	case err != nil && strings.HasPrefix(rest, "b/"):
		return target{err: invalidf("The request path is malformed.")}
	case err != nil || !path.Objects || path.Object != "" || path.Rest != "":
		return target{err: errNotFoundPath}
	}
	q := r.URL.Query()
	t := target{bucket: path.Bucket, object: q.Get("name")}
	if t.uploadID = q.Get("upload_id"); t.uploadID != "" {
		switch r.Method {
		case http.MethodPut, http.MethodPost, http.MethodDelete:
			t.route = routeSession
		default:
			t.err = errNotFoundPath
		}
		return t
	}
	if r.Method != http.MethodPost {
		return target{err: errNotFoundPath}
	}
	ut := q.Get("uploadType")
	if ut == "" {
		// Without uploadType: X-Goog-Upload-Protocol: multipart, else a media upload, as the
		// conformance tests expect of GCS (CT, gcs.endpoints.test.ts:158-173 and 306-318) and
		// the emulator does. No official client leaves it out (ADR-023 §3-91).
		switch p := r.Header.Get(wire.UploadProtocol); p {
		case "":
			ut = "media"
		case "multipart":
			ut = p
		default:
			return target{err: invalidf("Invalid %s for an upload without uploadType: %q.", wire.UploadProtocol, p)}
		}
	}
	switch ut {
	case "media":
		t.route = routeUploadMedia
	case "multipart":
		t.route = routeUploadMultipart
	case "resumable":
		t.route = routeStartResumable
	default:
		return target{err: invalidf("Invalid upload type: %q.", ut)}
	}
	if hasEncryptionHeaders(r.Header) {
		return target{err: errDeferred}
	}
	return t
}

// route serves a request: it classifies it, resolves its bucket, takes a place under
// upload_requests_per_ip for a write, admits the caller (a session needs no credential), and
// hands the handler the bucket, the name and the admin principal. Every refusal before the
// handler comes before any object is looked up, so a caller refused on a bucket learns nothing
// of what is in it (ADR-022 §14).
func (a *API) route(w http.ResponseWriter, r *http.Request) {
	t := classify(r)
	if t.err != nil {
		a.writeError(w, t.err, "", "")
		return
	}
	b, err := a.svc.Bucket(r.Context(), t.bucket)
	if err != nil {
		a.writeError(w, err, t.bucket, "")
		return
	}
	if t.route.writes() {
		release, ok := a.writes.Acquire(a.clientIP(r), a.svc.Limits().UploadRequestsPerIP)
		if !ok {
			a.writeError(w, errTooManyWrites, "", "")
			return
		}
		defer release()
	}
	if t.route == routeSession {
		a.resumeUpload(w, r, b, t.object, t.uploadID)
		return
	}
	p, err := a.admit(r, b, t.route.perm())
	if err != nil {
		a.writeError(w, err, b.Name, "")
		return
	}
	switch t.route {
	case routeGetBucket:
		a.getBucket(w, r, b, p)
	case routePatchBucket:
		a.patchBucket(w, r, b, p)
	case routeList:
		a.listObjects(w, r, b, p)
	case routeGetObject:
		a.getObject(w, r, b, t.object, p)
	case routePatchObject:
		a.patchObject(w, r, b, t.object, p)
	case routeDeleteObject:
		a.deleteObject(w, r, b, t.object, p)
	case routeRewrite:
		a.rewrite(w, r, b, t.object, t.dest, p)
	case routeDownload:
		a.download(w, r, b, t.object, p)
	case routeUploadMedia:
		a.uploadMedia(w, r, b, t.object, p)
	case routeUploadMultipart:
		a.uploadMultipart(w, r, b, t.object, p)
	case routeStartResumable:
		a.startResumable(w, r, b, t.object, p)
	}
}

// clientIP is the client's address, for the limits per IP: X-Forwarded-For from a trusted proxy
// alone. The first X-Forwarded-For from a peer that is not one is logged once: behind a proxy
// left out of trusted_proxies every client looks like the proxy (ADR-022 §17).
func (a *API) clientIP(r *http.Request) netip.Addr {
	ip := httpx.ClientIP(r, a.opts.TrustedProxies)
	if r.Header.Get("X-Forwarded-For") != "" && !a.warnedForwarded.Load() {
		if peer, err := netip.ParseAddrPort(r.RemoteAddr); err == nil && ip == peer.Addr().Unmap() &&
			!a.warnedForwarded.Swap(true) {
			a.log.Warn("gcs: X-Forwarded-For from a peer that is not a trusted proxy; the limits per IP count the proxy",
				"peer", peer.Addr().String())
		}
	}
	return ip
}

// hasEncryptionHeaders reports a customer-supplied encryption key (CSEK), which v0.3 defers.
func hasEncryptionHeaders(h http.Header) bool {
	for k := range h {
		k = strings.ToLower(k)
		if strings.HasPrefix(k, "x-goog-encryption-") || strings.HasPrefix(k, "x-goog-copy-source-encryption-") {
			return true
		}
	}
	return false
}

// writeJSON writes a JSON reply. Like every reply of the JSON API in production, it is not to
// be cached (bucket_probe.json).
func (a *API) writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := wire.Marshal(v)
	if err != nil {
		a.log.Error("gcs: encode a reply", "error", err)
		status = http.StatusInternalServerError
		body = []byte(`{"error":{"code":500,"message":"Internal error.","errors":[{"domain":"global","reason":"backendError","message":"Internal error."}]}}`)
	}
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=UTF-8")
	h.Set("Cache-Control", "no-cache, no-store, max-age=0, must-revalidate")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// readJSON reads a request's JSON body, at most json_body_bytes (ADR-022 §17). An empty body is
// nil.
func (a *API) readJSON(r *http.Request) ([]byte, error) {
	limit := a.svc.Limits().JSONBodyBytes
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	switch {
	case err != nil:
		return nil, invalidf("The request body could not be read.")
	case int64(len(body)) > limit:
		return nil, &apiError{status: http.StatusRequestEntityTooLarge, reason: wire.ReasonInvalid,
			message: "The request body is larger than " + strconv.FormatInt(limit, 10) + " bytes."}
	}
	return body, nil
}

// generation reads ?generation=: 0 when absent, the current generation. Any other than the
// current is not found, since v0.3 keeps no versions (ADR-023 §3-10).
func generation(q url.Values) (int64, error) {
	return positive(q, "generation")
}

// preconditions reads GCS's ifGenerationMatch, ifGenerationNotMatch, ifMetagenerationMatch and
// ifMetagenerationNotMatch from the query (B §10.2-8); ifGenerationMatch=0 is "must not exist".
func preconditions(q url.Values) (core.Preconditions, error) {
	return prefixedPreconditions(q, "if")
}

// sourcePreconditions reads a rewrite's ifSourceGenerationMatch and the like.
func sourcePreconditions(q url.Values) (core.Preconditions, error) {
	return prefixedPreconditions(q, "ifSource")
}

func prefixedPreconditions(q url.Values, prefix string) (core.Preconditions, error) {
	var pre core.Preconditions
	for _, f := range []struct {
		name string
		dst  **int64
	}{
		{"GenerationMatch", &pre.IfGenerationMatch}, {"GenerationNotMatch", &pre.IfGenerationNotMatch},
		{"MetagenerationMatch", &pre.IfMetagenerationMatch}, {"MetagenerationNotMatch", &pre.IfMetagenerationNotMatch},
	} {
		name := prefix + f.name
		v := q.Get(name)
		if v == "" {
			continue
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			return core.Preconditions{}, invalidf("Invalid value for %s: %q.", name, v)
		}
		*f.dst = new(n)
	}
	return pre, nil
}

// positive reads a parameter that is a positive integer when present, 0 when absent.
func positive(q url.Values, name string) (int64, error) {
	v := q.Get(name)
	if v == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return 0, invalidf("Invalid value for %s: %q.", name, v)
	}
	return n, nil
}

// flag reads a boolean parameter: absent, empty and "false" are false (B §4.2).
func flag(q url.Values, name string) (bool, error) {
	switch v := q.Get(name); v {
	case "", "false":
		return false, nil
	case "true":
		return true, nil
	default:
		return false, invalidf("Invalid value for %s: %q.", name, v)
	}
}
