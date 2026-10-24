package gcs

import (
	"net/http"

	"github.com/arumadev/aruma/internal/services/storage/core"
)

// The uploads of the GCS surface (S4-B; ADR-022 §6.2, B §5): POST /upload/storage/v1/b/<bucket>/o
// with uploadType media, multipart or resumable, and the requests on a resumable session.
//
// routeUpload has resolved the bucket and, for the three starts, authenticated the caller as a
// service account of the bucket's project (storage.objects.create). name is the query's name
// parameter, decoded once with form rules, "" when absent (a multipart upload may carry it in
// its JSON instead, B §5.2). The handlers answer every refusal with writeError, write resources
// with writeJSON and object, and read the metadata JSON with readJSON and decodeAttrs, the
// preconditions with preconditions.

// uploadMedia stores the body as the object's bytes: uploadType=media.
func (a *API) uploadMedia(w http.ResponseWriter, r *http.Request, b core.Bucket, name string, p core.Principal) {
	panic("S4-B: uploadMedia")
}

// uploadMultipart stores a multipart/related body, its JSON metadata then its bytes:
// uploadType=multipart (Node's resumable:false, Go under 16 MiB).
func (a *API) uploadMultipart(w http.ResponseWriter, r *http.Request, b core.Bucket, name string, p core.Principal) {
	panic("S4-B: uploadMultipart")
}

// startResumable starts a session: uploadType=resumable without an upload_id. Its reply carries
// the absolute session URL in Location (wire.GCSUploadURL from PublicURL).
func (a *API) startResumable(w http.ResponseWriter, r *http.Request, b core.Bucket, name string, p core.Principal) {
	panic("S4-B: startResumable")
}

// resumeUpload serves a request on a session: any PUT, POST or DELETE with an upload_id,
// whatever its uploadType (B §5.3). The upload_id is the credential, and Authorization is
// ignored (ADR-022 §7). name is the query's name, as the session URL carries it.
func (a *API) resumeUpload(w http.ResponseWriter, r *http.Request, b core.Bucket, name, id string) {
	panic("S4-B: resumeUpload")
}
