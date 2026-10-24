package gcs

import (
	"net/http"

	"github.com/arumadev/aruma/internal/services/storage/core"
)

// getObject is objects.get.
func (a *API) getObject(w http.ResponseWriter, r *http.Request, b core.Bucket, name string, p core.Principal) {
	a.writeError(w, errDeferred, "", "")
}

// listObjects is objects.list.
func (a *API) listObjects(w http.ResponseWriter, r *http.Request, b core.Bucket, p core.Principal) {
	a.writeError(w, errDeferred, "", "")
}

// patchObject is objects.patch.
func (a *API) patchObject(w http.ResponseWriter, r *http.Request, b core.Bucket, name string, p core.Principal) {
	a.writeError(w, errDeferred, "", "")
}

// deleteObject is objects.delete.
func (a *API) deleteObject(w http.ResponseWriter, r *http.Request, b core.Bucket, name string, p core.Principal) {
	a.writeError(w, errDeferred, "", "")
}

// rewrite is objects.rewrite; dest is the escaped b/<bucket>/o/<object> after rewriteTo/.
func (a *API) rewrite(w http.ResponseWriter, r *http.Request, b core.Bucket, name, dest string, p core.Principal) {
	a.writeError(w, errDeferred, "", "")
}
