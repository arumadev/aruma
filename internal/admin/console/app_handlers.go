package console

import (
	"bytes"
	"encoding/json"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/arumadev/aruma/internal/admin/render"
	"github.com/arumadev/aruma/internal/configgen"
	"github.com/arumadev/aruma/internal/platform/apierr"
	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/platform/httpx"
	"github.com/arumadev/aruma/internal/tenancy/core"
)

func (a *API) appRoutes(with func(authn.Policy, string, string, http.HandlerFunc)) {
	const app = "/console/v1/projects/{project}/apps/{app}"
	with(anyCred, "GET", "/console/v1/projects/{project}/apps", a.listApps)
	with(anyCred, "POST", "/console/v1/projects/{project}/apps", a.createApp)
	with(anyCred, "GET", app, a.getApp)
	with(anyCred, "PATCH", app, a.updateApp)
	with(anyCred, "POST", app+":remove", a.removeApp)
	with(anyCred, "POST", app+":undelete", a.undeleteApp)
	with(anyCred, "GET", app+"/config", a.appConfig)
	with(anyCred, "GET", app+"/sha", a.listSha)
	with(anyCred, "POST", app+"/sha", a.addSha)
	with(anyCred, "DELETE", app+"/sha/{sha}", a.deleteSha)
	with(anyCred, "GET", "/console/v1/projects/{project}/flutterOptions", a.flutterOptions)
	with(anyCred, "GET", "/console/v1/projects/{project}/adminSdkConfig", a.adminSdkConfig)
}

type appJSON struct {
	Name        string     `json:"name"`
	AppID       string     `json:"appId"`
	Platform    string     `json:"platform"`
	DisplayName string     `json:"displayName"`
	PackageName string     `json:"packageName,omitempty"`
	BundleID    string     `json:"bundleId,omitempty"`
	APIKeyID    string     `json:"apiKeyId,omitempty"`
	State       string     `json:"state"`
	ETag        string     `json:"etag"`
	CreateTime  time.Time  `json:"createTime"`
	UpdateTime  time.Time  `json:"updateTime"`
	DeleteTime  *time.Time `json:"deleteTime,omitempty"`
	ExpireTime  *time.Time `json:"expireTime,omitempty"`
}

func toAppJSON(x core.App) appJSON {
	j := appJSON{
		Name: "projects/" + x.ProjectID + "/apps/" + x.ID, AppID: x.ID, Platform: x.Platform, DisplayName: x.DisplayName,
		PackageName: x.PackageName, BundleID: x.BundleID, State: x.State, ETag: x.ETag,
		CreateTime: x.CreatedAt, UpdateTime: x.UpdatedAt, DeleteTime: x.DeleteTime, ExpireTime: x.ExpireTime,
	}
	if x.APIKeyUID != nil {
		j.APIKeyID = x.APIKeyUID.String()
	}
	return j
}

func (a *API) listApps(w http.ResponseWriter, r *http.Request) {
	page, err := a.svc.ListApps(r.Context(), principal(r), r.PathValue("project"), r.URL.Query().Get("platform"), listOptions(r))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out := make([]appJSON, 0, len(page.Items))
	for _, x := range page.Items {
		out = append(out, toAppJSON(x))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"apps": out, "nextPageToken": page.NextPageToken})
}

func (a *API) createApp(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Platform    string `json:"platform"`
		DisplayName string `json:"displayName"`
		PackageName string `json:"packageName"`
		BundleID    string `json:"bundleId"`
	}
	if err := httpx.DecodeJSON(r, &in, true); err != nil {
		a.fail(w, r, err)
		return
	}
	x, err := a.svc.CreateApp(r.Context(), principal(r), r.PathValue("project"), core.NewApp{
		Platform: in.Platform, DisplayName: in.DisplayName, PackageName: in.PackageName, BundleID: in.BundleID,
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toAppJSON(x))
}

func (a *API) getApp(w http.ResponseWriter, r *http.Request) {
	x, err := a.svc.GetApp(r.Context(), principal(r), r.PathValue("project"), r.PathValue("app"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toAppJSON(x))
}

func (a *API) updateApp(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DisplayName *string `json:"displayName"`
		ETag        string  `json:"etag"`
		// Present only to refuse them explicitly (AC-13).
		PackageName *string `json:"packageName"`
		BundleID    *string `json:"bundleId"`
	}
	if err := httpx.DecodeJSON(r, &in, true); err != nil {
		a.fail(w, r, err)
		return
	}
	if in.PackageName != nil || in.BundleID != nil {
		a.fail(w, r, apierr.Invalid("packageName", "Package names and bundle IDs cannot change after the app is created."))
		return
	}
	x, err := a.svc.UpdateApp(r.Context(), principal(r), r.PathValue("project"), r.PathValue("app"), core.AppUpdate{DisplayName: in.DisplayName, ETag: in.ETag})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toAppJSON(x))
}

func (a *API) removeApp(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Immediate bool   `json:"immediate"`
		ETag      string `json:"etag"`
	}
	if err := httpx.DecodeJSON(r, &in, true); err != nil {
		a.fail(w, r, err)
		return
	}
	x, err := a.svc.RemoveApp(r.Context(), principal(r), r.PathValue("project"), r.PathValue("app"), core.RemoveOptions{Immediate: in.Immediate, ETag: in.ETag})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toAppJSON(x))
}

func (a *API) undeleteApp(w http.ResponseWriter, r *http.Request) {
	x, err := a.svc.UndeleteApp(r.Context(), principal(r), r.PathValue("project"), r.PathValue("app"), false)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toAppJSON(x))
}

// appConfig downloads an app's config file. ?format=snippet returns the JavaScript block of a
// web app instead of its JSON.
func (a *API) appConfig(w http.ResponseWriter, r *http.Request) {
	c, err := a.svc.AppConfig(r.Context(), principal(r), r.PathValue("project"), r.PathValue("app"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if r.URL.Query().Get("format") == "snippet" {
		if c.App.App.Platform != core.PlatformWeb {
			a.fail(w, r, apierr.Invalid("format", "Snippets exist for web apps only."))
			return
		}
		download(w, render.File{Name: "firebase-config.js", ContentType: "text/javascript; charset=utf-8", Body: render.WebSnippet(c)}, false)
		return
	}
	download(w, render.AppFile(c), true)
}

func (a *API) flutterOptions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	c, err := a.svc.FlutterConfig(r.Context(), principal(r), r.PathValue("project"), core.FlutterSelection{
		Web: q.Get("web"), Android: q.Get("android"), IOS: q.Get("ios"), MacOS: q.Get("macos"),
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	download(w, render.Flutter(c), true)
}

func (a *API) adminSdkConfig(w http.ResponseWriter, r *http.Request) {
	p, err := a.svc.GetProject(r.Context(), principal(r), r.PathValue("project"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, configgen.Admin(render.Project(p)))
}

// download writes a generated file. Config files are never cached.
func download(w http.ResponseWriter, f render.File, attachment bool) {
	w.Header().Set("Content-Type", f.ContentType)
	w.Header().Set("Cache-Control", "no-store")
	if attachment {
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": f.Name}))
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(f.Body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(f.Body)
}

type shaJSON struct {
	Name     string `json:"name"`
	ID       string `json:"id"`
	ShaHash  string `json:"shaHash"`
	CertType string `json:"certType"`
}

func toShaJSON(projectRef string, c core.ShaCertificate) shaJSON {
	return shaJSON{Name: "projects/" + projectRef + "/apps/" + c.AppID + "/sha/" + c.ID.String(), ID: c.ID.String(), ShaHash: c.Hash, CertType: c.CertType}
}

func (a *API) listSha(w http.ResponseWriter, r *http.Request) {
	list, err := a.svc.ListShaCertificates(r.Context(), principal(r), r.PathValue("project"), r.PathValue("app"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out := make([]shaJSON, 0, len(list))
	for _, c := range list {
		out = append(out, toShaJSON(r.PathValue("project"), c))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"certificates": out})
}

func (a *API) addSha(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ShaHash string `json:"shaHash"`
	}
	if err := httpx.DecodeJSON(r, &in, true); err != nil {
		a.fail(w, r, err)
		return
	}
	c, err := a.svc.AddShaCertificate(r.Context(), principal(r), r.PathValue("project"), r.PathValue("app"), in.ShaHash)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toShaJSON(r.PathValue("project"), c))
}

func (a *API) deleteSha(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "sha")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.svc.DeleteShaCertificate(r.Context(), principal(r), r.PathValue("project"), r.PathValue("app"), id); err != nil {
		a.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// keyRoutes registers API key management (D-08).
func (a *API) keyRoutes(with func(authn.Policy, string, string, http.HandlerFunc)) {
	const key = "/console/v1/projects/{project}/apiKeys/{key}"
	with(anyCred, "GET", "/console/v1/projects/{project}/apiKeys", a.listKeys)
	with(anyCred, "POST", "/console/v1/projects/{project}/apiKeys", a.createKey)
	with(anyCred, "GET", key, a.getKey)
	with(anyCred, "PATCH", key, a.updateKey)
	with(anyCred, "POST", key+":rotate", a.rotateKey)
	with(anyCred, "POST", key+":disable", a.setKeyEnabled(false))
	with(anyCred, "POST", key+":enable", a.setKeyEnabled(true))
}

type keyJSON struct {
	Name         string            `json:"name"`
	UID          string            `json:"uid"`
	KeyString    string            `json:"keyString"`
	DisplayName  string            `json:"displayName"`
	Restrictions core.Restrictions `json:"restrictions"`
	State        string            `json:"state"`
	Platform     string            `json:"platform,omitempty"`
	RotatedFrom  string            `json:"rotatedFrom,omitempty"`
	ETag         string            `json:"etag"`
	CreateTime   time.Time         `json:"createTime"`
	UpdateTime   time.Time         `json:"updateTime"`
}

func toKeyJSON(projectRef string, k core.APIKey) keyJSON {
	j := keyJSON{
		Name: "projects/" + projectRef + "/apiKeys/" + k.UID.String(), UID: k.UID.String(), KeyString: k.KeyString,
		DisplayName: k.DisplayName, Restrictions: k.Restrictions, State: k.State, Platform: k.Platform, ETag: k.ETag,
		CreateTime: k.CreatedAt, UpdateTime: k.UpdatedAt,
	}
	if k.RotatedFrom != nil {
		j.RotatedFrom = k.RotatedFrom.String()
	}
	return j
}

func keyUID(r *http.Request) (uuid.UUID, error) { return pathUUID(r, "key") }

func (a *API) listKeys(w http.ResponseWriter, r *http.Request) {
	list, err := a.svc.ListAPIKeys(r.Context(), principal(r), r.PathValue("project"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out := make([]keyJSON, 0, len(list))
	for _, k := range list {
		out = append(out, toKeyJSON(r.PathValue("project"), k))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"keys": out})
}

func (a *API) createKey(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DisplayName  string            `json:"displayName"`
		Restrictions core.Restrictions `json:"restrictions"`
	}
	if err := httpx.DecodeJSON(r, &in, true); err != nil {
		a.fail(w, r, err)
		return
	}
	k, err := a.svc.CreateAPIKey(r.Context(), principal(r), r.PathValue("project"), in.DisplayName, in.Restrictions)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toKeyJSON(r.PathValue("project"), k))
}

func (a *API) getKey(w http.ResponseWriter, r *http.Request) {
	uid, err := keyUID(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	k, err := a.svc.GetAPIKey(r.Context(), principal(r), r.PathValue("project"), uid)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toKeyJSON(r.PathValue("project"), k))
}

func (a *API) updateKey(w http.ResponseWriter, r *http.Request) {
	uid, err := keyUID(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var in struct {
		DisplayName  *string         `json:"displayName"`
		Restrictions json.RawMessage `json:"restrictions"`
		ETag         string          `json:"etag"`
	}
	if err := httpx.DecodeJSON(r, &in, true); err != nil {
		a.fail(w, r, err)
		return
	}
	u := core.APIKeyUpdate{DisplayName: in.DisplayName, ETag: in.ETag}
	if len(in.Restrictions) > 0 {
		var res core.Restrictions
		dec := json.NewDecoder(bytesReader(in.Restrictions))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&res); err != nil {
			a.fail(w, r, apierr.Invalid("restrictions", "Restrictions are malformed."))
			return
		}
		u.Restrictions = &res
	}
	k, err := a.svc.UpdateAPIKey(r.Context(), principal(r), r.PathValue("project"), uid, u)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toKeyJSON(r.PathValue("project"), k))
}

func (a *API) rotateKey(w http.ResponseWriter, r *http.Request) {
	uid, err := keyUID(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	k, err := a.svc.RotateAPIKey(r.Context(), principal(r), r.PathValue("project"), uid)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toKeyJSON(r.PathValue("project"), k))
}

func (a *API) setKeyEnabled(enabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid, err := keyUID(r)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		k, err := a.svc.SetAPIKeyEnabled(r.Context(), principal(r), r.PathValue("project"), uid, enabled)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, toKeyJSON(r.PathValue("project"), k))
	}
}

// serviceAccountRoutes registers service account management (D-09).
func (a *API) serviceAccountRoutes(with func(authn.Policy, string, string, http.HandlerFunc)) {
	const sa = "/console/v1/projects/{project}/serviceAccounts/{sa}"
	with(anyCred, "GET", "/console/v1/projects/{project}/serviceAccounts", a.listServiceAccounts)
	with(anyCred, "POST", "/console/v1/projects/{project}/serviceAccounts", a.createServiceAccount)
	with(anyCred, "DELETE", sa, a.deleteServiceAccount)
	with(anyCred, "GET", sa+"/keys", a.listServiceAccountKeys)
	with(anyCred, "POST", sa+"/keys", a.createServiceAccountKey)
	with(anyCred, "DELETE", sa+"/keys/{keyId}", a.deleteServiceAccountKey)
}

type serviceAccountJSON struct {
	Name        string    `json:"name"`
	ID          string    `json:"id"`
	Email       string    `json:"email"`
	UniqueID    string    `json:"uniqueId"`
	DisplayName string    `json:"displayName"`
	Disabled    bool      `json:"disabled"`
	CreateTime  time.Time `json:"createTime"`
}

func toServiceAccountJSON(s core.ServiceAccount) serviceAccountJSON {
	return serviceAccountJSON{Name: "projects/" + s.ProjectID + "/serviceAccounts/" + s.ID.String(), ID: s.ID.String(), Email: s.Email, UniqueID: s.UniqueID, DisplayName: s.DisplayName, Disabled: s.Disabled, CreateTime: s.CreatedAt}
}

func (a *API) listServiceAccounts(w http.ResponseWriter, r *http.Request) {
	list, err := a.svc.ListServiceAccounts(r.Context(), principal(r), r.PathValue("project"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out := make([]serviceAccountJSON, 0, len(list))
	for _, s := range list {
		out = append(out, toServiceAccountJSON(s))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"serviceAccounts": out})
}

func (a *API) createServiceAccount(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AccountID   string `json:"accountId"`
		DisplayName string `json:"displayName"`
	}
	if err := httpx.DecodeJSON(r, &in, true); err != nil {
		a.fail(w, r, err)
		return
	}
	s, err := a.svc.CreateServiceAccount(r.Context(), principal(r), r.PathValue("project"), in.AccountID, in.DisplayName)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toServiceAccountJSON(s))
}

func (a *API) deleteServiceAccount(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "sa")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.svc.DeleteServiceAccount(r.Context(), principal(r), r.PathValue("project"), id); err != nil {
		a.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type saKeyJSON struct {
	KeyID       string     `json:"keyId"`
	Algorithm   string     `json:"keyAlgorithm"`
	ValidAfter  time.Time  `json:"validAfterTime"`
	ValidBefore *time.Time `json:"validBeforeTime,omitempty"`
	Disabled    bool       `json:"disabled"`
	CreateTime  time.Time  `json:"createTime"`
}

func (a *API) listServiceAccountKeys(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "sa")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	list, err := a.svc.ListServiceAccountKeys(r.Context(), principal(r), r.PathValue("project"), id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out := make([]saKeyJSON, 0, len(list))
	for _, k := range list {
		out = append(out, saKeyJSON{KeyID: k.KeyID, Algorithm: k.Algorithm, ValidAfter: k.ValidAfter, ValidBefore: k.ValidBefore, Disabled: k.Disabled, CreateTime: k.CreatedAt})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"keys": out})
}

// createServiceAccountKey returns the key file as a download. It is the only time the private
// key exists outside the developer's hands; nothing of it is kept.
func (a *API) createServiceAccountKey(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "sa")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	k, err := a.svc.CreateServiceAccountKey(r.Context(), principal(r), r.PathValue("project"), id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	download(w, render.ServiceAccountKey(k, a.svc.Options().PublicURL), true)
}

func (a *API) deleteServiceAccountKey(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "sa")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.svc.DeleteServiceAccountKey(r.Context(), principal(r), r.PathValue("project"), id, r.PathValue("keyId")); err != nil {
		a.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }
