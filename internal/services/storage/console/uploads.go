package console

import (
	"context"
	"net/http"
	"strconv"

	"github.com/arumadev/aruma/internal/platform/apierr"
	"github.com/arumadev/aruma/internal/platform/httpx"
	"github.com/arumadev/aruma/internal/services/storage/core"
	"github.com/arumadev/aruma/internal/services/storage/wire"
)

// uploadRoutes registers the route that issues the dashboard's upload sessions.
func (h *handlers) uploadRoutes() {
	h.handle("POST", "/console/v1/projects/{project}/storage/uploads", h.issueUpload)
}

// uploadRequest is the small JSON that asks for an upload session: the object's name, its
// size, which is mandatory and bound to the session, and the metadata it is stored with.
type uploadRequest struct {
	Name               string            `json:"name"`
	Size               *int64            `json:"size"`
	ContentType        string            `json:"contentType"`
	ContentEncoding    string            `json:"contentEncoding"`
	ContentDisposition string            `json:"contentDisposition"`
	ContentLanguage    string            `json:"contentLanguage"`
	CacheControl       string            `json:"cacheControl"`
	Metadata           map[string]string `json:"metadata"`
}

// attrs is the metadata the session stores the object with: the fields set, nothing deleted.
func (u uploadRequest) attrs() core.Patch {
	p := core.Patch{Fields: map[string]*string{}}
	for name, v := range map[string]string{
		core.FieldContentType: u.ContentType, core.FieldContentEncoding: u.ContentEncoding,
		core.FieldContentDisposition: u.ContentDisposition, core.FieldContentLanguage: u.ContentLanguage,
		core.FieldCacheControl: u.CacheControl,
	} {
		if v != "" {
			p.Fields[name] = new(v)
		}
	}
	if len(u.Metadata) > 0 {
		p.Metadata = make(map[string]*string, len(u.Metadata))
		for k, v := range u.Metadata {
			p.Metadata[k] = new(v)
		}
	}
	return p
}

// issueUpload starts a resumable upload session for the dashboard (ADR-022 §15) and returns its
// /v0 URL, to which the dashboard sends the chunks with the X-Goog-Upload-* protocol and
// without the cookie. The session is an admin's, bound to the caller's account, which must
// still own the project when it finalizes; it lives console_session_ttl (an hour), and its
// size is declared here and bound. The issue is audited here, and the finalize by core.
//
// The URL carries the upload id, the session's only credential: it is in the reply's body
// alone (no-store), and neither the id nor the URL is logged or audited.
func (h *handlers) issueUpload(w http.ResponseWriter, r *http.Request) {
	t, err := h.target(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	var in uploadRequest
	if err := httpx.DecodeJSON(r, &in, true); err != nil {
		h.m.Fail(w, r, err)
		return
	}
	switch {
	case in.Name == "":
		h.m.Fail(w, r, apierr.Invalid("name", "The object's name is required."))
		return
	case in.Size == nil:
		h.m.Fail(w, r, apierr.Invalid("size", "The upload's size is required: a dashboard upload declares it."))
		return
	case *in.Size < 0:
		h.m.Fail(w, r, apierr.Invalid("size", "The upload's size must not be negative."))
		return
	}
	ip := httpx.ClientAddr(r.Context())
	if ok, wait := h.svc.AllowUploadStart(ip); !ok {
		h.svc.Metrics().Rejections.WithLabelValues("start_rate").Inc()
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		h.m.Fail(w, r, apierr.New(apierr.ResourceExhausted, "Too many uploads started. Try again later."))
		return
	}
	now := h.svc.Now()
	id, err := h.svc.StartUpload(r.Context(), t.bucket, t.as, core.UploadSpec{
		Name: in.Name, Attrs: in.attrs(), Declared: *in.Size, Surface: core.SurfaceConsole, IP: ip,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if err := h.audit(r, t, "storage.upload.issue", in.Name, map[string]any{"size": *in.Size}); err != nil {
		// A session the log does not show is not handed out: it is cancelled, and its id never
		// leaves the server.
		err = apierr.Wrap(err, apierr.Internal, "The audit log did not record the upload; its session was cancelled.")
		if _, cerr := h.svc.CancelUpload(context.WithoutCancel(r.Context()), t.bucket, in.Name, id); cerr != nil {
			err = apierr.Wrap(cerr, apierr.Internal, "The audit log did not record the upload; its session expires unused.")
		}
		h.m.Fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"uploadUrl":  h.sessionURL(t.bucket.Name, in.Name, id),
		"bucket":     t.bucket.Name,
		"name":       in.Name,
		"size":       *in.Size,
		"expireTime": wire.Time(now.Add(h.svc.Limits().ConsoleSessionTTL)),
	})
}

// sessionURL is a session's /v0 URL, absolute from public_url and in the shape the start of a
// client's upload answers with (ADR-022 §6.1).
func (h *handlers) sessionURL(bucket, name, id string) string {
	return h.opts.PublicURL + "/v0/b/" + wire.EscapeComponent(bucket) + "/o?name=" + wire.EscapeComponent(name) +
		"&upload_id=" + wire.EscapeComponent(id) + "&upload_protocol=resumable"
}
