package api

import (
	"net/http"

	"github.com/arumadev/aruma/internal/services/storage/core"
)

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
	panic("S3-B: upload")
}

// uploadSession serves a POST or PUT on an upload session URL (?upload_id=): a chunk, a query,
// a cancel or a finalize, by X-Goog-Upload-Command (A §3.11-3.13). id is the session id from the
// query and name the object's (uploadName). There is no principal: Authorization on a session
// URL is ignored, since the URL itself is the credential and iOS sends none (ADR-022 §7).
//
// The router has resolved the bucket, set the API's CORS headers and taken the request's place
// under upload_requests_per_ip.
func (a *API) uploadSession(w http.ResponseWriter, r *http.Request, b core.Bucket, name, id string) {
	panic("S3-B: uploadSession")
}
