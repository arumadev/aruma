package mgmt

import (
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/osama300/aurma-project/internal/admin/render"
	"github.com/osama300/aurma-project/internal/configgen"
	"github.com/osama300/aurma-project/internal/platform/apierr"
	"github.com/osama300/aurma-project/internal/platform/httpx"
	"github.com/osama300/aurma-project/internal/tenancy/core"
)

// M-01
func (a *API) listProjects(w http.ResponseWriter, r *http.Request) {
	opt, err := listOptions(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	page, err := a.svc.ListProjects(r.Context(), principal(r), opt)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out := make([]firebaseProject, 0, len(page.Items))
	for _, p := range page.Items {
		out = append(out, toProject(p))
	}
	body := map[string]any{"results": out}
	if page.NextPageToken != "" {
		body["nextPageToken"] = page.NextPageToken
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

// M-02. Deleted projects are not visible through this API.
func (a *API) getProject(w http.ResponseWriter, r *http.Request) {
	p, err := a.svc.GetProject(r.Context(), principal(r), r.PathValue("project"))
	if err == nil && p.State != "ACTIVE" {
		err = apierr.New(apierr.NotFound, "Requested entity was not found.")
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toProject(p))
}

// M-03
func (a *API) patchProject(w http.ResponseWriter, r *http.Request) {
	mask := updateMask(r)
	if len(mask) == 0 {
		a.fail(w, r, apierr.Invalid("updateMask", "updateMask is required."))
		return
	}
	if err := checkMask(mask, "displayName"); err != nil {
		a.fail(w, r, err)
		return
	}
	var in struct {
		DisplayName string `json:"displayName"`
		ETag        string `json:"etag"`
	}
	if err := httpx.DecodeJSON(r, &in, false); err != nil {
		a.fail(w, r, err)
		return
	}
	p, err := a.svc.UpdateProject(r.Context(), principal(r), r.PathValue("project"), core.ProjectUpdate{DisplayName: &in.DisplayName, ETag: in.ETag})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toProject(p))
}

// M-04: every project of the caller, for project pickers.
func (a *API) availableProjects(w http.ResponseWriter, r *http.Request) {
	opt, err := listOptions(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	page, err := a.svc.ListProjects(r.Context(), principal(r), opt)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out := make([]projectInfo, 0, len(page.Items))
	for _, p := range page.Items {
		out = append(out, projectInfo{Project: "projects/" + p.ID, DisplayName: p.DisplayName})
	}
	body := map[string]any{"projectInfo": out}
	if page.NextPageToken != "" {
		body["nextPageToken"] = page.NextPageToken
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

// M-05. The filter supports the AIP-160 subset clients use: equality on platform, app_id and
// namespace, joined with AND.
func (a *API) searchApps(w http.ResponseWriter, r *http.Request) {
	opt, err := listOptions(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	f, err := parseFilter(r.URL.Query().Get("filter"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	page, err := a.svc.ListApps(r.Context(), principal(r), r.PathValue("project"), f.platform, opt)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out := make([]appInfo, 0, len(page.Items))
	for _, x := range page.Items {
		if f.match(x) {
			out = append(out, toAppInfo(x))
		}
	}
	body := map[string]any{"apps": out}
	if page.NextPageToken != "" {
		body["nextPageToken"] = page.NextPageToken
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

type appFilter struct{ platform, appID, namespace string }

func (f appFilter) match(x core.App) bool {
	return (f.appID == "" || x.ID == f.appID) && (f.namespace == "" || x.Namespace() == f.namespace)
}

func parseFilter(s string) (appFilter, error) {
	var f appFilter
	if strings.TrimSpace(s) == "" {
		return f, nil
	}
	for _, term := range strings.Split(s, " AND ") {
		k, v, ok := strings.Cut(term, "=")
		if !ok {
			k, v, ok = strings.Cut(term, ":")
		}
		if !ok {
			return f, apierr.Invalid("filter", "Unsupported filter. Use field=value terms joined with AND.")
		}
		k = strings.TrimSpace(k)
		v = strings.Trim(strings.TrimSpace(v), `"`)
		switch k {
		case "platform":
			f.platform = strings.ToUpper(v)
		case "app_id", "appId":
			f.appID = v
		case "namespace":
			f.namespace = v
		default:
			return f, apierr.Invalid("filter", "Unsupported filter field "+k+".")
		}
	}
	return f, nil
}

// M-18
func (a *API) adminSdkConfig(w http.ResponseWriter, r *http.Request) {
	p, err := a.svc.GetProject(r.Context(), principal(r), r.PathValue("project"))
	if err == nil && p.State != "ACTIVE" {
		err = apierr.New(apierr.NotFound, "Requested entity was not found.")
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, configgen.Admin(render.Project(p)))
}

// appOf fetches an app and checks it belongs to the collection in the URL.
func (a *API) appOf(r *http.Request, platform string) (core.App, error) {
	x, err := a.svc.GetApp(r.Context(), principal(r), r.PathValue("project"), r.PathValue("app"))
	if err != nil {
		return x, err
	}
	if x.Platform != platform {
		return x, apierr.New(apierr.NotFound, "Requested entity was not found.")
	}
	return x, nil
}

// render returns an app's resource, including SHA hashes for Android apps.
func (a *API) render(r *http.Request, x core.App) (string, any, error) {
	var shas []core.ShaCertificate
	if x.Platform == core.PlatformAndroid && x.State == "ACTIVE" {
		var err error
		if shas, err = a.svc.ListShaCertificates(r.Context(), principal(r), x.ProjectID, x.ID); err != nil {
			return "", nil, err
		}
	}
	typ, res := appResource(x, shas)
	return typ, res, nil
}

// M-06, M-09, M-12
func (a *API) listApps(platform string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		opt, err := listOptions(r)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		page, err := a.svc.ListApps(r.Context(), principal(r), r.PathValue("project"), platform, opt)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		out := make([]any, 0, len(page.Items))
		for _, x := range page.Items {
			_, res, err := a.render(r, x)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			out = append(out, res)
		}
		body := map[string]any{"apps": out}
		if page.NextPageToken != "" {
			body["nextPageToken"] = page.NextPageToken
		}
		httpx.WriteJSON(w, http.StatusOK, body)
	}
}

// create returns an Operation that is already done, as the firebase CLI then polls it.
func (a *API) createApp(platform string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			DisplayName string `json:"displayName"`
			PackageName string `json:"packageName"`
			BundleID    string `json:"bundleId"`
		}
		if err := httpx.DecodeJSON(r, &in, false); err != nil {
			a.fail(w, r, err)
			return
		}
		x, err := a.svc.CreateApp(r.Context(), principal(r), r.PathValue("project"), core.NewApp{
			Platform: platform, DisplayName: in.DisplayName, PackageName: in.PackageName, BundleID: in.BundleID,
		})
		if err != nil {
			a.fail(w, r, err)
			return
		}
		a.writeOperation(w, r, x)
	}
}

func (a *API) writeOperation(w http.ResponseWriter, r *http.Request, x core.App) {
	typ, res, err := a.render(r, x)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, operation{
		Name: operationName(opRef{Kind: "app", Name: appName(x)}), Done: true, Response: withType(typePrefix+typ, res),
	})
}

// M-17
func (a *API) getOperation(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ref, ok := parseOperation(name)
	notFound := apierr.New(apierr.NotFound, "Requested entity was not found.")
	if !ok {
		a.fail(w, r, notFound)
		return
	}
	full := "operations/" + name
	if ref.Kind == "empty" {
		httpx.WriteJSON(w, http.StatusOK, operation{Name: full, Done: true, Response: map[string]string{"@type": "type.googleapis.com/google.protobuf.Empty"}})
		return
	}
	// projects/{p}/{collection}/{app}
	parts := strings.Split(ref.Name, "/")
	if ref.Kind != "app" || len(parts) != 4 || parts[0] != "projects" {
		a.fail(w, r, notFound)
		return
	}
	x, err := a.svc.GetApp(r.Context(), principal(r), parts[1], parts[3])
	if err != nil {
		a.fail(w, r, err)
		return
	}
	typ, res, err := a.render(r, x)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, operation{Name: full, Done: true, Response: withType(typePrefix+typ, res)})
}

// M-07, M-10, M-13
func (a *API) getApp(platform string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		x, err := a.appOf(r, platform)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		_, res, err := a.render(r, x)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, res)
	}
}

func (a *API) patchApp(platform string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mask := updateMask(r)
		if err := checkMask(mask, "displayName"); err != nil {
			a.fail(w, r, err)
			return
		}
		var in struct {
			DisplayName *string `json:"displayName"`
			ETag        string  `json:"etag"`
		}
		if err := httpx.DecodeJSON(r, &in, false); err != nil {
			a.fail(w, r, err)
			return
		}
		if len(mask) > 0 && !contains(mask, "displayName") {
			in.DisplayName = nil
		}
		if _, err := a.appOf(r, platform); err != nil {
			a.fail(w, r, err)
			return
		}
		x, err := a.svc.UpdateApp(r.Context(), principal(r), r.PathValue("project"), r.PathValue("app"), core.AppUpdate{DisplayName: in.DisplayName, ETag: in.ETag})
		if err != nil {
			a.fail(w, r, err)
			return
		}
		_, res, err := a.render(r, x)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, res)
	}
}

// M-08, M-11, M-14. The web config is written byte for byte as the console serves it (AC-21);
// native files travel base64-encoded.
func (a *API) getConfig(platform string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, err := a.appOf(r, platform); err != nil {
			a.fail(w, r, err)
			return
		}
		c, err := a.svc.AppConfig(r.Context(), principal(r), r.PathValue("project"), r.PathValue("app"))
		if err != nil {
			a.fail(w, r, err)
			return
		}
		f := render.AppFile(c)
		if platform == core.PlatformWeb {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(f.Body)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, nativeConfig{ConfigFilename: f.Name, ConfigFileContents: f.Body})
	}
}

// M-16
func (a *API) removeApp(platform string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ValidateOnly bool   `json:"validateOnly"`
			AllowMissing bool   `json:"allowMissing"`
			Immediate    bool   `json:"immediate"`
			ETag         string `json:"etag"`
		}
		if err := httpx.DecodeJSON(r, &in, false); err != nil {
			a.fail(w, r, err)
			return
		}
		if x, err := a.appOf(r, platform); err != nil && !(in.AllowMissing && apierr.CodeOf(err) == apierr.NotFound) {
			a.fail(w, r, err)
			return
		} else if err == nil && x.State != "ACTIVE" && !in.Immediate {
			a.fail(w, r, apierr.New(apierr.FailedPrecondition, "The app is already deleted."))
			return
		}
		x, err := a.svc.RemoveApp(r.Context(), principal(r), r.PathValue("project"), r.PathValue("app"), core.RemoveOptions{
			Immediate: in.Immediate, AllowMissing: in.AllowMissing, ValidateOnly: in.ValidateOnly, ETag: in.ETag,
		})
		if err != nil {
			a.fail(w, r, err)
			return
		}
		if in.Immediate || x.ProjectID == "" {
			httpx.WriteJSON(w, http.StatusOK, operation{
				Name: operationName(opRef{Kind: "empty"}), Done: true,
				Response: map[string]string{"@type": "type.googleapis.com/google.protobuf.Empty"},
			})
			return
		}
		a.writeOperation(w, r, x)
	}
}

func (a *API) undeleteApp(platform string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ValidateOnly bool `json:"validateOnly"`
		}
		if err := httpx.DecodeJSON(r, &in, false); err != nil {
			a.fail(w, r, err)
			return
		}
		if _, err := a.appOf(r, platform); err != nil {
			a.fail(w, r, err)
			return
		}
		x, err := a.svc.UndeleteApp(r.Context(), principal(r), r.PathValue("project"), r.PathValue("app"), in.ValidateOnly)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		a.writeOperation(w, r, x)
	}
}

// M-15
func (a *API) listSha(w http.ResponseWriter, r *http.Request) {
	x, err := a.appOf(r, core.PlatformAndroid)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	list, err := a.svc.ListShaCertificates(r.Context(), principal(r), x.ProjectID, x.ID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out := make([]shaCertificate, 0, len(list))
	for _, c := range list {
		out = append(out, toSha(x.ProjectID, x.ID, c))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"certificates": out})
}

func (a *API) createSha(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ShaHash  string `json:"shaHash"`
		CertType string `json:"certType"`
	}
	if err := httpx.DecodeJSON(r, &in, false); err != nil {
		a.fail(w, r, err)
		return
	}
	x, err := a.appOf(r, core.PlatformAndroid)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	c, err := a.svc.AddShaCertificate(r.Context(), principal(r), x.ProjectID, x.ID, in.ShaHash)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if in.CertType != "" && in.CertType != "SHA_CERTIFICATE_TYPE_UNSPECIFIED" && in.CertType != c.CertType {
		_ = a.svc.DeleteShaCertificate(r.Context(), principal(r), x.ProjectID, x.ID, c.ID)
		a.fail(w, r, apierr.Invalid("certType", "certType does not match the length of shaHash."))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toSha(x.ProjectID, x.ID, c))
}

func (a *API) deleteSha(w http.ResponseWriter, r *http.Request) {
	x, err := a.appOf(r, core.PlatformAndroid)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	id, err := uuid.Parse(r.PathValue("sha"))
	if err != nil {
		a.fail(w, r, apierr.New(apierr.NotFound, "Requested entity was not found."))
		return
	}
	if err := a.svc.DeleteShaCertificate(r.Context(), principal(r), x.ProjectID, x.ID, id); err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{})
}
