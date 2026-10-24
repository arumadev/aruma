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
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/services/storage/core"
	"github.com/arumadev/aruma/internal/services/storage/wire"
	tcore "github.com/arumadev/aruma/internal/tenancy/core"
)

// Projects lets an admin credential prove it may act on a project: the control plane's
// ownership gate.
type Projects interface {
	GetProject(ctx context.Context, p *authn.Principal, ref string) (tcore.Project, error)
}

// Options configure the API.
type Options struct {
	// PublicURL is the server's address without a trailing slash: the session Location and
	// the resources' links are absolute from it.
	PublicURL string
	// Credentials verifies the Bearer access tokens of service accounts.
	Credentials authn.Verifier
	// Projects checks that a credential may act on the bucket's project.
	Projects Projects
}

// API is the GCS JSON endpoint.
type API struct {
	svc  *core.Service
	opts Options
	log  *slog.Logger
}

// New returns the API.
func New(svc *core.Service, opts Options, log *slog.Logger) *API {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	opts.PublicURL = strings.TrimSuffix(opts.PublicURL, "/")
	return &API{svc: svc, opts: opts, log: log}
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

func (a *API) route(w http.ResponseWriter, r *http.Request) {
	escaped := r.URL.EscapedPath()
	if rest, ok := strings.CutPrefix(escaped, wire.GCSUploadPrefix); ok {
		a.routeUpload(w, r, rest)
		return
	}
	if rest, ok := strings.CutPrefix(escaped, wire.GCSDownloadPrefix); ok {
		a.routeJSON(w, r, rest, true)
		return
	}
	if rest, ok := strings.CutPrefix(escaped, wire.GCSPrefix); ok {
		a.routeJSON(w, r, rest, false)
		return
	}
	a.writeError(w, errNotFoundPath, "", "")
}

// routeJSON routes the JSON API under /storage/v1/, and under /download/storage/v1/, which
// serves an object's metadata and media alone (its mediaLink, B §10.1).
func (a *API) routeJSON(w http.ResponseWriter, r *http.Request, rest string, download bool) {
	q := r.URL.Query()
	alt := q.Get("alt")
	if alt != "" && alt != "json" && alt != "media" {
		a.writeError(w, invalidf("Invalid alt parameter: %q.", alt), "", "")
		return
	}
	if rest == "b" || rest == "b/" || strings.HasPrefix(rest, "projects/") {
		// buckets.list and insert, HMAC keys, the project's service account.
		a.writeError(w, errDeferred, "", "")
		return
	}
	path, err := wire.SplitObjectPath(rest)
	if err != nil {
		if strings.HasPrefix(rest, "b/") {
			a.writeError(w, invalidf("The request path is malformed."), "", "")
		} else {
			a.writeError(w, errNotFoundPath, "", "")
		}
		return
	}
	media := alt == "media"
	object := path.Objects && path.Object != "" && path.Rest == ""
	if (download && !object) || (media && !(object && (r.Method == http.MethodGet || r.Method == http.MethodHead))) {
		if media && !download {
			a.writeError(w, invalidf("alt=media is only for reading an object."), "", "")
		} else {
			a.writeError(w, errNotFoundPath, "", "")
		}
		return
	}
	if hasEncryptionHeaders(r.Header) {
		a.writeError(w, errDeferred, "", "") // CSEK (ADR-023 §2)
		return
	}

	switch {
	case !path.Objects && path.Rest == "":
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			a.serveBucket(w, r, path.Bucket, permBucketsGet, a.getBucket)
		case http.MethodPatch:
			a.serveBucket(w, r, path.Bucket, permBucketsUpdate, a.patchBucket)
		case http.MethodPut, http.MethodDelete:
			a.writeError(w, errDeferred, "", "") // buckets.update, buckets.delete
		default:
			a.writeError(w, errNotFoundPath, "", "")
		}
	case !path.Objects:
		part, _, _ := strings.Cut(path.Rest, "/")
		if deferredBucketParts[part] {
			a.writeError(w, errDeferred, "", "")
		} else {
			a.writeError(w, errNotFoundPath, "", "")
		}
	case path.Object == "" && path.Rest == "":
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			a.serveBucket(w, r, path.Bucket, permObjectsList, a.listObjects)
		case http.MethodPost:
			a.writeError(w, errDeferred, "", "") // objects.insert without a body: uploads go to /upload
		default:
			a.writeError(w, errNotFoundPath, "", "")
		}
	case path.Rest == "":
		switch {
		case (r.Method == http.MethodGet || r.Method == http.MethodHead) && media:
			a.serveObject(w, r, path.Bucket, path.Object, permObjectsGet, a.download)
		case r.Method == http.MethodGet || r.Method == http.MethodHead:
			a.serveObject(w, r, path.Bucket, path.Object, permObjectsGet, a.getObject)
		case download:
			a.writeError(w, errNotFoundPath, "", "")
		case r.Method == http.MethodPatch:
			a.serveObject(w, r, path.Bucket, path.Object, permObjectsUpdate, a.patchObject)
		case r.Method == http.MethodDelete:
			a.serveObject(w, r, path.Bucket, path.Object, permObjectsDelete, a.deleteObject)
		case r.Method == http.MethodPut:
			a.writeError(w, errDeferred, "", "") // objects.update: the SDKs PATCH
		default:
			a.writeError(w, errNotFoundPath, "", "")
		}
	default:
		part, dest, _ := strings.Cut(path.Rest, "/")
		switch {
		case part == "rewriteTo" && r.Method == http.MethodPost:
			a.serveObject(w, r, path.Bucket, path.Object, permObjectsGet, func(w http.ResponseWriter, r *http.Request, b core.Bucket, name string, p core.Principal) {
				a.rewrite(w, r, b, name, dest, p)
			})
		case deferredObjectParts[part]:
			a.writeError(w, errDeferred, "", "")
		default:
			a.writeError(w, errNotFoundPath, "", "")
		}
	}
}

// routeUpload routes /upload/storage/v1/b/<bucket>/o. An upload_id makes the request one on an
// existing session whatever its method and uploadType, since Go sends its chunks as POST to a
// URL that carries uploadType=resumable (B §5.3, ADR-022 §6.2); its Authorization is ignored.
// Without one, uploadType picks the protocol.
func (a *API) routeUpload(w http.ResponseWriter, r *http.Request, rest string) {
	path, err := wire.SplitObjectPath(rest)
	switch {
	case err != nil && strings.HasPrefix(rest, "b/"):
		a.writeError(w, invalidf("The request path is malformed."), "", "")
		return
	case err != nil || !path.Objects || path.Object != "" || path.Rest != "":
		a.writeError(w, errNotFoundPath, "", "")
		return
	}
	q := r.URL.Query()
	name := q.Get("name")
	if id := q.Get("upload_id"); id != "" {
		switch r.Method {
		case http.MethodPut, http.MethodPost, http.MethodDelete:
		default:
			a.writeError(w, errNotFoundPath, "", "")
			return
		}
		b, err := a.svc.Bucket(r.Context(), path.Bucket)
		if err != nil {
			a.writeError(w, err, path.Bucket, "")
			return
		}
		a.resumeUpload(w, r, b, name, id)
		return
	}
	if r.Method != http.MethodPost {
		a.writeError(w, errNotFoundPath, "", "")
		return
	}
	var start objectHandler
	switch t := q.Get("uploadType"); t {
	case "media":
		start = a.uploadMedia
	case "multipart":
		start = a.uploadMultipart
	case "resumable":
		start = a.startResumable
	case "":
		a.writeError(w, requiredf("Upload requests must include an uploadType URL parameter and a URL-encoded object name."), "", "")
		return
	default:
		a.writeError(w, invalidf("Invalid upload type: %q.", t), "", "")
		return
	}
	if hasEncryptionHeaders(r.Header) {
		a.writeError(w, errDeferred, "", "")
		return
	}
	b, p, ok := a.admit(w, r, path.Bucket, permObjectsCreate)
	if !ok {
		return
	}
	start(w, r, b, name, p)
}

// bucketHandler serves a request on a bucket once it is resolved and the caller admitted.
type bucketHandler func(w http.ResponseWriter, r *http.Request, b core.Bucket, p core.Principal)

// objectHandler serves a request on an object once its bucket is resolved and the caller
// admitted; name is the object's name, decoded once.
type objectHandler func(w http.ResponseWriter, r *http.Request, b core.Bucket, name string, p core.Principal)

func (a *API) serveBucket(w http.ResponseWriter, r *http.Request, bucket, perm string, h bucketHandler) {
	b, p, ok := a.admit(w, r, bucket, perm)
	if ok {
		h(w, r, b, p)
	}
}

func (a *API) serveObject(w http.ResponseWriter, r *http.Request, bucket, name, perm string, h objectHandler) {
	b, p, ok := a.admit(w, r, bucket, perm)
	if ok {
		h(w, r, b, name, p)
	}
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
