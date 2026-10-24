package api

import (
	"net/http"

	"github.com/arumadev/aruma/internal/services/storage/core"
)

// download serves GET (or HEAD) /o/{object}?alt=media: the object's bytes, opened with
// ?token= or by the rules through core.Service.Open, then media.Serve (A §3.3, ADR-022 §9, §10).
// name is the path's object, decoded once; p is the request's principal (anonymous without a
// credential, which a token link needs none of).
//
// The router has resolved the bucket, authenticated the request, and set the bucket's CORS
// headers for the request's Origin (mediaCORS, cors.go): the API's open CORS is not on a
// download. Errors go through writeError.
func (a *API) download(w http.ResponseWriter, r *http.Request, b core.Bucket, name string, p core.Principal) {
	panic("S3-C: download")
}
