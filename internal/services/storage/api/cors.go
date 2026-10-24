package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/arumadev/aruma/internal/services/storage/core"
	"github.com/arumadev/aruma/internal/services/storage/wire"
)

// CORS on /v0 (ADR-022 §11). Two policies:
//
//   - The API (metadata, list, uploads, PATCH, DELETE, their errors): open to every origin,
//     Access-Control-Allow-Origin: * and no Allow-Credentials, since the credential is a header,
//     never a cookie. Production sends * on /v0 (bucket_probe.json), and Firebase's uploads and
//     metadata reads work from a browser without any CORS setting. The web SDK reads the upload
//     headers below, which a browser hides from a cross-origin page unless they are exposed
//     (A §6.1). The preflight is 204 with the methods and headers the clients send (ADR-023
//     §3-27), a fixed list: never wider than what an SDK needs.
//   - alt=media: the bucket's own cors[] rules (core.Service.MatchCORS), none by default, as a
//     new Firebase bucket: a page on another origin cannot read an object's bytes until the
//     owner allows its origin. <img> and <video> need no CORS.

// apiExposed are the response headers a cross-origin page may read on the API's replies: the
// upload protocol's, which JS reads (A §6.1), and Content-Range and ETag as production and A §7
// expose them.
var apiExposed = strings.Join([]string{"Content-Range", "ETag", wire.UploadURL, wire.UploadStatus,
	wire.UploadSizeReceived, "X-Goog-Upload-Chunk-Granularity"}, ", ")

// apiAllowedHeaders are the request headers the three SDKs send (A §2.3, §3, §6.1).
var apiAllowedHeaders = strings.Join([]string{"Authorization", "Content-Type", "Range",
	"X-Firebase-Storage-Version", "X-Firebase-AppCheck", "X-Firebase-GMPID", wire.HTTPMethodOverride,
	wire.UploadProtocol, wire.UploadCommand, wire.UploadOffset, wire.UploadHeaderContentLength,
	wire.UploadHeaderContentType, wire.UploadContentLength, wire.UploadContentType}, ", ")

// apiAllowedMethods are the methods of /v0 (A §3.1, §7).
const apiAllowedMethods = "GET, POST, PUT, PATCH, DELETE"

// preflightMaxAge is how long a browser may keep the API's preflight, in seconds.
const preflightMaxAge = "3600"

// apiCORS sets the API's CORS headers on a reply that is not a download's.
func apiCORS(h http.Header) {
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Expose-Headers", apiExposed)
}

// preflight answers an OPTIONS request: 204, with the API's methods and headers, or with what
// the bucket's matching rule allows for a download (mediaCORS has set them already).
func (a *API) preflight(w http.ResponseWriter, r *http.Request, media bool) {
	if !media {
		h := w.Header()
		h.Set("Access-Control-Allow-Methods", apiAllowedMethods)
		h.Set("Access-Control-Allow-Headers", apiAllowedHeaders)
		h.Set("Access-Control-Max-Age", preflightMaxAge)
	}
	w.WriteHeader(http.StatusNoContent)
}

// mediaCORS sets the CORS headers of a download, or of its preflight, from the bucket's rules:
// the first rule whose origins and methods hold the request's decides, as in GCS. Without an
// Origin, or without a rule that matches, it sets none, and a browser refuses the page the
// bytes. Vary: Origin is always set, so a cache never gives one origin another's answer.
//
// As in GCS, a rule's responseHeader is both what a page may read (Expose-Headers) and, on a
// preflight, what the request may send (Allow-Headers). The method allowed is the one asked for.
func (a *API) mediaCORS(w http.ResponseWriter, r *http.Request, b core.Bucket) error {
	h := w.Header()
	h.Add("Vary", "Origin")
	origin := r.Header.Get("Origin")
	if origin == "" {
		return nil
	}
	method := r.Method
	preflight := method == http.MethodOptions
	if preflight {
		method = r.Header.Get("Access-Control-Request-Method")
	}
	rules, err := a.svc.CORS(r.Context(), b)
	if err != nil {
		return err
	}
	allow, rule := a.svc.MatchCORS(rules, origin, method)
	if !allow {
		return nil
	}
	h.Set("Access-Control-Allow-Origin", origin)
	if len(rule.ResponseHeaders) > 0 {
		h.Set("Access-Control-Expose-Headers", strings.Join(rule.ResponseHeaders, ", "))
	}
	if preflight {
		h.Set("Access-Control-Allow-Methods", method)
		if len(rule.ResponseHeaders) > 0 {
			h.Set("Access-Control-Allow-Headers", strings.Join(rule.ResponseHeaders, ", "))
		}
		if rule.MaxAgeSeconds > 0 {
			h.Set("Access-Control-Max-Age", strconv.Itoa(rule.MaxAgeSeconds))
		}
	}
	return nil
}
