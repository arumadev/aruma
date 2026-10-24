package gcs

import (
	"net/http"

	"github.com/arumadev/aruma/internal/services/storage/core"
)

// download serves an object's bytes (S4-C; ADR-022 §10.5, §14, B §6.1): GET or HEAD with
// alt=media on /storage/v1/b/<bucket>/o/<object>, and on /download/storage/v1/..., the
// mediaLink. routeJSON has resolved the bucket and authenticated the caller as a service
// account of its project (storage.objects.get); name is the object's name, decoded once from
// the escaped path. It reads ?generation= with generation and the preconditions with
// preconditions, and answers every refusal before the bytes with writeError.
func (a *API) download(w http.ResponseWriter, r *http.Request, b core.Bucket, name string, p core.Principal) {
	panic("S4-C: download")
}
