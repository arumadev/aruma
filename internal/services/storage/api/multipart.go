package api

import (
	"net/http"

	"github.com/arumadev/aruma/internal/services/storage/core"
)

// multipart serves a POST or PUT on /o or /o/{object} with X-Goog-Upload-Protocol: multipart:
// one multipart/related body, its JSON part then the bytes, through wire.NewRelatedReader into
// core.Insert (A §3.9). name is ?name= decoded once, else the path's object (uploadName), and ""
// when neither names it: the JSON part's name may (ADR-023 §3-26).
//
// The router has resolved the bucket, authenticated the request, set the API's CORS headers and
// taken the request's place under upload_requests_per_ip.
func (a *API) multipart(w http.ResponseWriter, r *http.Request, b core.Bucket, name string, p core.Principal) {
	panic("S3-B: multipart")
}
