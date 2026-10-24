package api

import (
	"cmp"
	"net/http"
	"strconv"

	"github.com/arumadev/aruma/internal/services/storage/core"
	"github.com/arumadev/aruma/internal/services/storage/wire"
)

// list serves GET /o: one page of the bucket's prefixes and objects, after the rules allow a list
// (A §3.7, ADR-022 §13). The query is prefix (empty or absent is the root), delimiter (the SDKs
// always send "/"), maxResults and pageToken; an empty value is absent. GCS's startOffset and
// endOffset are not /v0's and are ignored.
//
// The reply is {"prefixes":[...],"items":[{"name","bucket"}],"nextPageToken":"..."}: both lists
// present even when empty, items of two strings (iOS drops a page otherwise), the prefixes with
// their trailing delimiter as GCS writes them (the SDKs strip it), and no nextPageToken on the
// last page, since an empty one loops listAll forever (A §1.8). Names with an empty segment are
// left out by core on this surface.
func (a *API) list(w http.ResponseWriter, r *http.Request, b core.Bucket, p core.Principal) {
	q := r.URL.Query()
	o := core.ListOptions{Prefix: q.Get("prefix"), Delimiter: q.Get("delimiter"), PageToken: q.Get("pageToken"),
		Surface: core.SurfaceFirebase}
	if v := q.Get("maxResults"); v != "" {
		// 1 to 1000, which the SDKs check themselves (A §3.7); larger is cut to the limit by
		// core (ADR-023 §3-5).
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			a.writeError(w, r, statusError(http.StatusBadRequest, "maxResults must be a positive integer."))
			return
		}
		o.MaxResults = n
	}
	page, err := a.svc.List(r.Context(), b, p, o)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	reply := wire.FirebaseList{NextPageToken: page.NextPageToken, Prefixes: page.Prefixes,
		Items: make([]wire.FirebaseListItem, len(page.Objects))}
	for i, obj := range page.Objects {
		reply.Items[i] = wire.FirebaseListItem{Name: obj.Name, Bucket: cmp.Or(obj.Bucket, b.Name)}
	}
	writeJSON(w, http.StatusOK, reply)
}
