package console

import (
	"net/http"

	"github.com/arumadev/aruma/internal/platform/httpx"
	"github.com/arumadev/aruma/internal/services/storage/core"
)

// settingsRoutes registers the bucket's settings and its usage.
func (h *handlers) settingsRoutes() {
	const base = "/console/v1/projects/{project}/storage"
	h.handle("GET", base+"/usage", h.getUsage)
	h.handle("GET", base+"/cors", h.getCORS)
	h.handle("PUT", base+"/cors", h.setCORS)
}

// getUsage returns the project's storage use: the bytes its objects hold, the bytes its upload
// sessions have reserved, its objects, and its quota (0 is unlimited).
func (h *handlers) getUsage(w http.ResponseWriter, r *http.Request) {
	t, err := h.target(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	u, err := h.svc.Usage(r.Context(), t.bucket)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"usedBytes": u.UsedBytes, "reservedBytes": u.ReservedBytes, "objects": u.Objects, "quotaBytes": u.QuotaBytes,
	})
}

// corsJSON is one CORS rule with GCS's field names (cors[] of a bucket), which gsutil and
// gcloud files use too (ADR-022 §11).
type corsJSON struct {
	Origin         []string `json:"origin"`
	Method         []string `json:"method"`
	ResponseHeader []string `json:"responseHeader"`
	MaxAgeSeconds  int      `json:"maxAgeSeconds"`
}

type corsBody struct {
	CORS []corsJSON `json:"cors"`
}

func toCORSJSON(rules []core.CORSRule) corsBody {
	out := corsBody{CORS: []corsJSON{}}
	for _, r := range rules {
		c := corsJSON{Origin: r.Origins, Method: r.Methods, ResponseHeader: r.ResponseHeaders, MaxAgeSeconds: r.MaxAgeSeconds}
		for _, l := range []*[]string{&c.Origin, &c.Method, &c.ResponseHeader} {
			if *l == nil {
				*l = []string{}
			}
		}
		out.CORS = append(out.CORS, c)
	}
	return out
}

// getCORS returns the bucket's CORS rules, which decide alt=media from other origins; none until
// the owner sets them, as a new Firebase bucket.
func (h *handlers) getCORS(w http.ResponseWriter, r *http.Request) {
	t, err := h.target(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	rules, err := h.svc.CORS(r.Context(), t.bucket)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toCORSJSON(rules))
}

// setCORS replaces the bucket's CORS rules; an empty list removes them all.
func (h *handlers) setCORS(w http.ResponseWriter, r *http.Request) {
	t, err := h.target(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	var in corsBody
	if err := httpx.DecodeJSON(r, &in, true); err != nil {
		h.m.Fail(w, r, err)
		return
	}
	rules := make([]core.CORSRule, 0, len(in.CORS))
	for _, c := range in.CORS {
		rules = append(rules, core.CORSRule{Origins: c.Origin, Methods: c.Method, ResponseHeaders: c.ResponseHeader,
			MaxAgeSeconds: c.MaxAgeSeconds})
	}
	info, err := h.svc.SetCORS(r.Context(), t.bucket, t.as, rules)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if err := h.audit(r, t, "storage.bucket.cors.update", t.bucket.Name, map[string]any{"rules": len(info.CORS)}); err != nil {
		h.m.Fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toCORSJSON(info.CORS))
}
