package api

import (
	"net/http"

	"github.com/arumadev/aruma/internal/services/storage/core"
)

// list serves GET /o: one page of the bucket's prefixes and objects.
func (a *API) list(w http.ResponseWriter, r *http.Request, b core.Bucket, p core.Principal) {
	panic("S3-A: list")
}
