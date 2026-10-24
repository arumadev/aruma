package api

import (
	"net/http"

	"github.com/arumadev/aruma/internal/services/storage/core"
)

// metadata serves GET /o/{object}: the object's resource.
func (a *API) metadata(w http.ResponseWriter, r *http.Request, b core.Bucket, name string, p core.Principal) {
	panic("S3-A: metadata")
}

// update serves PATCH /o/{object}, and Android's PUT with X-HTTP-Method-Override: PATCH.
func (a *API) update(w http.ResponseWriter, r *http.Request, b core.Bucket, name string, p core.Principal) {
	panic("S3-A: update")
}

// delete serves DELETE /o/{object}.
func (a *API) delete(w http.ResponseWriter, r *http.Request, b core.Bucket, name string, p core.Principal) {
	panic("S3-A: delete")
}

// tokens serves POST /o/{object}?create_token=true and ?delete_token=<t>.
func (a *API) tokens(w http.ResponseWriter, r *http.Request, b core.Bucket, name string, p core.Principal) {
	panic("S3-A: tokens")
}
