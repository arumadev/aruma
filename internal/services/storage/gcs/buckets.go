package gcs

import (
	"net/http"

	"github.com/arumadev/aruma/internal/services/storage/core"
)

// getBucket is buckets.get.
func (a *API) getBucket(w http.ResponseWriter, r *http.Request, b core.Bucket, p core.Principal) {
	a.writeError(w, errDeferred, "", "")
}

// patchBucket is buckets.patch.
func (a *API) patchBucket(w http.ResponseWriter, r *http.Request, b core.Bucket, p core.Principal) {
	a.writeError(w, errDeferred, "", "")
}
