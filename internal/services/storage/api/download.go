package api

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/arumadev/aruma/internal/services/storage/core"
	"github.com/arumadev/aruma/internal/services/storage/media"
	"github.com/arumadev/aruma/internal/services/storage/wire"
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
	// A token decides alone, and one that matches none is 403 without the rules (ADR-022 §9,
	// ADR-023 §3-19). An empty ?token= is no token, as an empty parameter is absent elsewhere
	// (ADR-022 §13): the rules decide, as they would with the parameter left out.
	token := r.URL.Query().Get("token")
	o, body, err := a.svc.Open(r.Context(), b, name, p, token, 0)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	defer body.Close()

	// A link with a token is its own credential, and serves the stored Cache-Control as is. Any
	// other download is the rules' decision for this request's principal, which a shared cache
	// must not keep for someone else: private, also for an anonymous one (ADR-023 §3-69).
	mode := media.RulesDecided
	if token != "" {
		mode = media.Token
	}
	lim := a.svc.Limits()
	err = media.Serve(w, r, mediaItem(o), body, mode, media.Limits{
		DecompressedBytes: lim.ObjectBytes,
		MinBytesPerSecond: lim.MinBytesPerSecond,
		Cut: func(err error) {
			if errors.Is(err, media.ErrDecompressedTooLarge) {
				a.svc.Metrics().DecompressionsCut.Inc()
				a.log.Warn("storage: a decompressed download passed object_bytes", "project", b.Project)
			}
		},
	})
	if err != nil {
		// Before the status line: the object's file did not seek, which is the server's fault.
		a.writeError(w, r, fmt.Errorf("%w: serve %q: %w", core.ErrUnavailable, name, err))
	}
}

// mediaItem is what media.Serve needs of an object. The ETag follows the generation alone, so a
// metadata change never restarts a download in progress (wire.MediaETag).
func mediaItem(o core.Object) media.Item {
	return media.Item{
		Name:               o.Name,
		ContentType:        o.ContentType,
		ContentEncoding:    o.ContentEncoding,
		ContentDisposition: o.ContentDisposition,
		ContentLanguage:    o.ContentLanguage,
		CacheControl:       o.CacheControl,
		Size:               o.Size,
		ETag:               wire.MediaETag(o.Generation),
		Updated:            o.Updated,
		Generation:         o.Generation,
		Metageneration:     o.Metageneration,
		CRC32C:             o.CRC32C,
		MD5:                o.MD5,
	}
}
