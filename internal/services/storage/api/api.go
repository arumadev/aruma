// Package api serves Cloud Storage to the Firebase client SDKs over Firebase REST: /v0/b/... and
// the prefixed form /firebasestorage.googleapis.com/v0/b/... that a runtime redirect produces
// (ADR-022 §1). It routes on URL.EscapedPath, so an encoded "/" stays inside an object's name,
// and every reply is free of JSON null (ADR-022 §12).
//
// The files and their owners (storage-layout.md): api.go routes, auth.go, errors.go, cors.go,
// objects.go, list.go and resource.go are S3-A's; upload.go and multipart.go S3-B's; download.go
// S3-C's. The upload and download handlers are called with what the router has settled (the
// bucket, the object's name, the principal or the session id) and use the shared helpers of
// errors.go and resource.go.
package api

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"

	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/platform/httpx"
	"github.com/arumadev/aruma/internal/platform/idtoken"
	"github.com/arumadev/aruma/internal/platform/ratelimit"
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
	// PublicURL is the server's address without a trailing slash: the upload session URL is
	// absolute from it, never from the Host header (ADR-022 §6.1).
	PublicURL string
	// TrustedProxies are the proxies whose X-Forwarded-For is believed, for the limits per IP.
	TrustedProxies []netip.Prefix
	// Credentials verifies the Bearer access tokens of Admin SDKs and the CLI.
	Credentials authn.Verifier
	// Projects checks that a credential may act on the bucket's project.
	Projects Projects
	// IDTokens verifies the "Authorization: Firebase <idToken>" of signed-in users.
	IDTokens idtoken.Verifier
	// Writes counts the write requests in flight per client address under
	// upload_requests_per_ip (ADR-022 §17). It is one budget per address, which the GCS surface
	// shares (app.go gives both the same); nil gives this API a count of its own.
	Writes *ratelimit.InFlight
}

// API is the Firebase REST endpoint.
type API struct {
	svc  *core.Service
	opts Options
	log  *slog.Logger

	// writes counts the write requests in flight per client address (upload_requests_per_ip,
	// ADR-022 §17): uploads, upload session requests, PATCH, DELETE and the token calls, never
	// the reads. It is opts.Writes, or a count of its own.
	writes *ratelimit.InFlight
	// warnedForwarded is set once the log has warned of an X-Forwarded-For from a peer that is
	// not a trusted proxy (ADR-022 §17).
	warnedForwarded atomic.Bool
}

// New returns the API.
func New(svc *core.Service, opts Options, log *slog.Logger) *API {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &API{svc: svc, opts: opts, log: log, writes: cmp.Or(opts.Writes, new(ratelimit.InFlight))}
}

// Handler returns the handler of every request under the two /v0 prefixes.
func (a *API) Handler() http.Handler { return http.HandlerFunc(a.serve) }

// route is what a /v0 request asks for, from its method, path, query and headers alone.
type route uint8

const (
	routeNone      route = iota // no operation: 400 "Invalid HTTP method/URL pair."
	routePreflight              // OPTIONS: a CORS preflight
	routeList                   // GET /o
	routeMetadata               // GET /o/{object}
	routeDownload               // GET /o/{object}?alt=media (download.go)
	routeUpdate                 // PATCH /o/{object}, or PUT with X-HTTP-Method-Override: PATCH
	routeDelete                 // DELETE /o/{object}
	routeTokens                 // POST /o/{object}?create_token=true or ?delete_token=<t>
	routeUpload                 // POST or PUT on /o or /o/{object}: a resumable start or a media upload (upload.go)
	routeMultipart              // the same with X-Goog-Upload-Protocol: multipart (multipart.go)
	routeSession                // POST or PUT with ?upload_id=: a request on an upload session (upload.go)
)

var routeNames = [...]string{"none", "preflight", "list", "metadata", "download", "update", "delete", "tokens",
	"upload", "multipart", "session"}

func (r route) String() string { return routeNames[r] }

// writes reports whether a route writes, so it counts against upload_requests_per_ip.
func (r route) writes() bool {
	switch r {
	case routeUpdate, routeDelete, routeTokens, routeUpload, routeMultipart, routeSession:
		return true
	}
	return false
}

// media reports whether a request is for an object's bytes, whose CORS is the bucket's (ADR-022
// §11): a download, or the preflight of one.
func (r route) media(q url.Values) bool {
	return r == routeDownload || r == routePreflight && q.Get("alt") == "media"
}

// classify finds a request's route. The object is p.Object, a single path segment that every
// client escapes (A §2.2); a path that goes on after it, or stops before /o, names nothing.
//
// What decides, from A §3.1, §6.7 and §7 and the emulator's routes (D §1.3):
//   - ?upload_id= is a session URL, which the clients reach with POST (JS, Android) or PUT
//     (iOS), whatever else the request carries.
//   - An upload is any other POST or PUT, on /o (JS, Android) or on /o/{object} (iOS), told
//     multipart by X-Goog-Upload-Protocol alone: uploadType is ignored (A §6.7), as JS never
//     sends it.
//   - PUT with X-HTTP-Method-Override: PATCH is Android's updateMetadata, and a POST that names
//     create_token or delete_token is the console's token call (CT, E §5).
//   - alt=media is a download; no alt, or alt=json, is the metadata. HEAD is served as GET.
func classify(r *http.Request, p wire.ObjectPath, q url.Values) route {
	if !p.Objects || p.Rest != "" {
		return routeNone
	}
	method := r.Method
	if method == http.MethodHead {
		method = http.MethodGet
	}
	if method == http.MethodOptions {
		return routePreflight
	}
	if q.Has("upload_id") {
		if method == http.MethodPost || method == http.MethodPut {
			return routeSession
		}
		return routeNone
	}
	object := p.Object != ""
	switch method {
	case http.MethodGet:
		switch {
		case !object:
			return routeList
		case q.Get("alt") == "media":
			return routeDownload
		case !q.Has("alt") || q.Get("alt") == "json":
			return routeMetadata
		}
		return routeNone
	case http.MethodPatch:
		if object {
			return routeUpdate
		}
		return routeNone
	case http.MethodDelete:
		if object {
			return routeDelete
		}
		return routeNone
	case http.MethodPut:
		if strings.EqualFold(r.Header.Get(wire.HTTPMethodOverride), http.MethodPatch) {
			if object {
				return routeUpdate
			}
			return routeNone
		}
	case http.MethodPost:
		if object && (q.Has("create_token") || q.Has("delete_token")) {
			return routeTokens
		}
	default:
		return routeNone
	}
	if strings.EqualFold(r.Header.Get(wire.UploadProtocol), "multipart") {
		return routeMultipart
	}
	return routeUpload
}

// serve routes one request. In order, as production answers (bucket_probe.json): a path or
// method that names no operation is 400 whatever the bucket; then a bucket that is no active
// project's is 404, before any credential is looked at; then the credential. Every reply but a
// download's carries the API's open CORS headers, errors included; a download's are the
// bucket's (cors.go).
func (a *API) serve(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var (
		path wire.ObjectPath
		rt   = routeNone
	)
	q := r.URL.Query()
	if rest, ok := wire.CutFirebasePrefix(r.URL.EscapedPath()); ok {
		if p, err := wire.SplitObjectPath(rest); err == nil {
			path, rt = p, classify(r, p, q)
		}
	}
	media := rt.media(q)
	if !media {
		apiCORS(w.Header())
	}
	if rt == routeNone {
		a.writeError(w, r, errBadRoute)
		return
	}
	b, err := a.svc.Bucket(ctx, path.Bucket)
	if err != nil {
		if rt == routeUpload && resumable(r.Header) && errors.Is(err, core.ErrBucketNotFound) {
			// Production ends a resumable start on an unknown bucket (and not a multipart
			// upload) with X-Goog-Upload-Status: final (bucket_probe.json).
			wire.Set(w.Header(), wire.UploadStatus, wire.StatusFinal)
		}
		a.writeError(w, r, err)
		return
	}
	if media {
		if err := a.mediaCORS(w, r, b); err != nil {
			a.writeError(w, r, err)
			return
		}
	}
	if rt == routePreflight {
		a.preflight(w, r, media)
		return
	}
	if rt.writes() {
		release, ok := a.writes.Acquire(a.clientIP(r), a.svc.Limits().UploadRequestsPerIP)
		if !ok {
			a.writeError(w, r, errTooManyWrites)
			return
		}
		defer release()
	}
	if rt == routeSession {
		// The session URL is the credential: Authorization on it is ignored (ADR-022 §7, iOS
		// sends none, A §1.1).
		a.uploadSession(w, r, b, uploadName(path, q), q.Get("upload_id"))
		return
	}
	p, err := a.principal(ctx, r, b)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	switch rt {
	case routeList:
		a.list(w, r, b, p)
	case routeMetadata:
		a.metadata(w, r, b, path.Object, p)
	case routeDownload:
		a.download(w, r, b, path.Object, p)
	case routeUpdate:
		a.update(w, r, b, path.Object, p)
	case routeDelete:
		a.delete(w, r, b, path.Object, p)
	case routeTokens:
		a.tokens(w, r, b, path.Object, p)
	case routeUpload:
		a.upload(w, r, b, uploadName(path, q), p)
	case routeMultipart:
		a.multipart(w, r, b, uploadName(path, q), p)
	}
}

// uploadName is the name an upload request gives its object: ?name=, decoded once, which every
// client sends, else the path's object (iOS puts it in both; A §6.8, the emulator's order). It is
// "" when neither has one: a multipart body may still name it (ADR-023 §3-26).
func uploadName(p wire.ObjectPath, q url.Values) string {
	return cmp.Or(q.Get("name"), p.Object)
}

// clientIP is the client's address, for the limits per IP: X-Forwarded-For from a trusted proxy
// alone. The first X-Forwarded-For from a peer that is not one is logged once: behind a proxy
// left out of trusted_proxies every client looks like the proxy (ADR-022 §17).
func (a *API) clientIP(r *http.Request) netip.Addr {
	ip := httpx.ClientIP(r, a.opts.TrustedProxies)
	if r.Header.Get("X-Forwarded-For") != "" && !a.warnedForwarded.Load() {
		if peer, err := netip.ParseAddrPort(r.RemoteAddr); err == nil && ip == peer.Addr().Unmap() &&
			!a.warnedForwarded.Swap(true) {
			a.log.Warn("storage: X-Forwarded-For from a peer that is not a trusted proxy; the limits per IP count the proxy",
				"peer", peer.Addr().String())
		}
	}
	return ip
}
