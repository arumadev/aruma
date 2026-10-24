package console

import (
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strconv"

	"github.com/arumadev/aruma/internal/platform/apierr"
	"github.com/arumadev/aruma/internal/platform/httpx"
	"github.com/arumadev/aruma/internal/services/storage/core"
	"github.com/arumadev/aruma/internal/services/storage/media"
	"github.com/arumadev/aruma/internal/services/storage/wire"
)

// objectRoutes registers the file browser's routes. The listing takes a folder's ?prefix=; the
// others take the object's ?name=, never a path segment, so any name reaches them as written
// (one ending in "/", or holding "//" or ":").
func (h *handlers) objectRoutes() {
	const base = "/console/v1/projects/{project}/storage"
	h.handle("GET", base+"/objects", h.browse)
	h.handle("GET", base+"/object", h.getObject)
	h.handle("PATCH", base+"/object", h.updateObject)
	h.handle("DELETE", base+"/object", h.deleteObject)
	h.handle("GET", base+"/object:download", h.download)
	h.handle("POST", base+"/object:createToken", h.createToken)
	h.handle("POST", base+"/object:deleteToken", h.deleteToken)
}

// defaultPage is a listing's page size when the dashboard names none.
const defaultPage = 100

// objectJSON is an object as the dashboard shows it. The 64-bit generations are strings, as
// Google's APIs write them; downloadUrls are the token links (ADR-022 §9), one per token.
type objectJSON struct {
	Name               string            `json:"name"`
	Bucket             string            `json:"bucket"`
	Size               int64             `json:"size"`
	ContentType        string            `json:"contentType,omitempty"`
	ContentEncoding    string            `json:"contentEncoding,omitempty"`
	ContentDisposition string            `json:"contentDisposition,omitempty"`
	ContentLanguage    string            `json:"contentLanguage,omitempty"`
	CacheControl       string            `json:"cacheControl,omitempty"`
	Metadata           map[string]string `json:"metadata"`
	DownloadTokens     []string          `json:"downloadTokens"`
	DownloadURLs       []string          `json:"downloadUrls"`
	Generation         string            `json:"generation"`
	Metageneration     string            `json:"metageneration"`
	MD5Hash            string            `json:"md5Hash"`
	CRC32C             string            `json:"crc32c"`
	TimeCreated        string            `json:"timeCreated"`
	Updated            string            `json:"updated"`
}

func (h *handlers) objectJSON(o core.Object) objectJSON {
	out := objectJSON{
		Name: o.Name, Bucket: o.Bucket, Size: o.Size,
		ContentType: o.ContentType, ContentEncoding: o.ContentEncoding, ContentDisposition: o.ContentDisposition,
		ContentLanguage: o.ContentLanguage, CacheControl: o.CacheControl,
		Metadata: o.Metadata, DownloadTokens: o.DownloadTokens, DownloadURLs: []string{},
		Generation: wire.Int(o.Generation), Metageneration: wire.Int(o.Metageneration),
		MD5Hash: wire.MD5(o.MD5), CRC32C: wire.CRC32C(o.CRC32C),
		TimeCreated: wire.Time(o.Created), Updated: wire.Time(o.Updated),
	}
	if out.Metadata == nil {
		out.Metadata = map[string]string{}
	}
	if out.DownloadTokens == nil {
		out.DownloadTokens = []string{}
	}
	for _, t := range o.DownloadTokens {
		out.DownloadURLs = append(out.DownloadURLs, h.opts.PublicURL+"/v0/b/"+wire.EscapeComponent(o.Bucket)+
			"/o/"+wire.EscapeComponent(o.Name)+"?alt=media&token="+wire.EscapeComponent(t))
	}
	return out
}

// objectName is the ?name= of a request, which must be present.
func objectName(r *http.Request) (string, error) {
	name := r.URL.Query().Get("name")
	if name == "" {
		return "", apierr.Invalid("name", "The object's name is required.")
	}
	return name, nil
}

// browse returns one page of the folder at ?prefix= ("" for the root, else ending in "/"): its
// subfolders and the objects directly in it. pageSize defaults to 100 and is at most
// list_max_results; pageToken continues the listing.
func (h *handlers) browse(w http.ResponseWriter, r *http.Request) {
	t, err := h.target(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	q := r.URL.Query()
	size := defaultPage
	if v := q.Get("pageSize"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			h.m.Fail(w, r, apierr.Invalid("pageSize", "pageSize must be a positive number."))
			return
		}
		size = min(n, h.svc.Limits().ListMaxResults)
	}
	page, err := h.svc.List(r.Context(), t.bucket, t.as, core.ListOptions{
		Prefix: q.Get("prefix"), Delimiter: "/", PageToken: q.Get("pageToken"), MaxResults: size,
		Surface: core.SurfaceConsole,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items := make([]objectJSON, 0, len(page.Objects))
	for _, o := range page.Objects {
		items = append(items, h.objectJSON(o))
	}
	prefixes := page.Prefixes
	if prefixes == nil {
		prefixes = []string{}
	}
	out := map[string]any{"prefixes": prefixes, "items": items}
	if page.NextPageToken != "" {
		out["nextPageToken"] = page.NextPageToken
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (h *handlers) getObject(w http.ResponseWriter, r *http.Request) {
	t, err := h.target(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	name, err := objectName(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	o, err := h.svc.Stat(r.Context(), t.bucket, name, t.as, core.SurfaceConsole, 0)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, h.objectJSON(o))
}

// The content fields a metadata edit may set, by their names on both APIs.
var patchFields = []string{core.FieldContentType, core.FieldContentEncoding, core.FieldContentDisposition,
	core.FieldContentLanguage, core.FieldCacheControl}

// decodePatch reads a metadata edit: the content fields and "metadata", each a value or null.
// A null field deletes it; "metadata": null clears the custom metadata, and a null key in it
// deletes that key, as PATCH does on both APIs. It returns the patch and the fields it names.
func decodePatch(r *http.Request) (core.Patch, []string, error) {
	var raw map[string]json.RawMessage
	if err := httpx.DecodeJSON(r, &raw, true); err != nil {
		return core.Patch{}, nil, err
	}
	p := core.Patch{Fields: map[string]*string{}}
	for _, key := range slices.Sorted(maps.Keys(raw)) {
		v := raw[key]
		switch {
		case key == "metadata":
			var md map[string]*string
			if err := json.Unmarshal(v, &md); err != nil {
				return core.Patch{}, nil, apierr.Invalid("metadata", "metadata must be an object of strings, or null.")
			}
			if md == nil {
				p.ClearMetadata = true
			}
			p.Metadata = md
		case slices.Contains(patchFields, key):
			var s *string
			if err := json.Unmarshal(v, &s); err != nil {
				return core.Patch{}, nil, apierr.Invalid(key, key+" must be a string, or null.")
			}
			p.Fields[key] = s
		default:
			return core.Patch{}, nil, apierr.Invalid(key, "Unknown field "+strconv.Quote(key)+".")
		}
	}
	return p, slices.Sorted(maps.Keys(raw)), nil
}

func (h *handlers) updateObject(w http.ResponseWriter, r *http.Request) {
	t, err := h.target(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	name, err := objectName(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	patch, fields, err := decodePatch(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	o, err := h.svc.Update(r.Context(), t.bucket, name, t.as, core.SurfaceConsole, patch, core.Preconditions{})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if err := h.audit(r, t, "storage.object.update", name, map[string]any{
		"fields": fields, "metageneration": wire.Int(o.Metageneration)}); err != nil {
		h.m.Fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, h.objectJSON(o))
}

func (h *handlers) deleteObject(w http.ResponseWriter, r *http.Request) {
	t, err := h.target(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	name, err := objectName(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	if err := h.svc.Delete(r.Context(), t.bucket, name, t.as, core.SurfaceConsole, core.Preconditions{}); err != nil {
		h.fail(w, r, err)
		return
	}
	if err := h.audit(r, t, "storage.object.delete", name, nil); err != nil {
		h.m.Fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, struct{}{})
}

// download serves an object's bytes to the dashboard through media in its Console mode: always
// attachment, never a page on the dashboard's origin, with every header media writes (ADR-022
// §10, §15). It rides the session cookie, so it keeps the general CORP same-origin.
func (h *handlers) download(w http.ResponseWriter, r *http.Request) {
	t, err := h.target(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	name, err := objectName(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	o, body, err := h.svc.Open(r.Context(), t.bucket, name, t.as, "", 0)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	defer body.Close()
	lim := h.svc.Limits()
	metrics := h.svc.Metrics()
	cw := &countingWriter{ResponseWriter: w}
	defer func() { metrics.Bytes.WithLabelValues("out", core.SurfaceConsole.String()).Add(float64(cw.n)) }()
	err = media.Serve(cw, r, mediaItem(o), body, media.Console, media.Limits{
		DecompressedBytes: lim.ObjectBytes,
		MinBytesPerSecond: lim.MinBytesPerSecond,
		Cut: func(err error) {
			if errors.Is(err, media.ErrDecompressedTooLarge) {
				metrics.DecompressionsCut.Inc()
			}
		},
	})
	if err != nil {
		// Before the status line: nothing was written.
		h.m.Fail(w, r, apierr.Wrap(err, apierr.Internal, "The object could not be read."))
	}
}

// mediaItem is what media needs to know of an object.
func mediaItem(o core.Object) media.Item {
	return media.Item{
		Name: o.Name, ContentType: o.ContentType, ContentEncoding: o.ContentEncoding,
		ContentDisposition: o.ContentDisposition, ContentLanguage: o.ContentLanguage, CacheControl: o.CacheControl,
		Size: o.Size, ETag: wire.MediaETag(o.Generation), Updated: o.Updated,
		Generation: o.Generation, Metageneration: o.Metageneration, CRC32C: o.CRC32C, MD5: o.MD5,
	}
}

// countingWriter counts the body bytes of a download, for core.Metrics.Bytes. It unwraps, so
// media's http.ResponseController reaches the connection's deadlines.
type countingWriter struct {
	http.ResponseWriter
	n int64
}

func (c *countingWriter) Write(b []byte) (int, error) {
	n, err := c.ResponseWriter.Write(b)
	c.n += int64(n)
	return n, err
}

func (c *countingWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

// createToken adds a download token to an object, which makes a new download link.
func (h *handlers) createToken(w http.ResponseWriter, r *http.Request) {
	t, err := h.target(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	name, err := objectName(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	o, err := h.svc.CreateToken(r.Context(), t.bucket, name, t.as)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	// The token is a credential: the log names the object, never the token.
	if err := h.audit(r, t, "storage.object.token.create", name, map[string]any{"tokens": len(o.DownloadTokens)}); err != nil {
		h.m.Fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, h.objectJSON(o))
}

// deleteToken revokes one of an object's download tokens, given in the body so that it never
// reaches a URL or a request log. Revoking the last one makes another at once (ADR-022 §9).
func (h *handlers) deleteToken(w http.ResponseWriter, r *http.Request) {
	t, err := h.target(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	name, err := objectName(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	var in struct {
		Token string `json:"token"`
	}
	if err := httpx.DecodeJSON(r, &in, true); err != nil {
		h.m.Fail(w, r, err)
		return
	}
	if in.Token == "" {
		h.m.Fail(w, r, apierr.Invalid("token", "The download token to revoke is required."))
		return
	}
	o, err := h.svc.DeleteToken(r.Context(), t.bucket, name, t.as, in.Token)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if err := h.audit(r, t, "storage.object.token.delete", name, map[string]any{"tokens": len(o.DownloadTokens)}); err != nil {
		h.m.Fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, h.objectJSON(o))
}
