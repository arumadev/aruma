package console

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/osama300/aurma-project/internal/platform/apierr"
	"github.com/osama300/aurma-project/internal/platform/authn"
	"github.com/osama300/aurma-project/internal/platform/httpx"
	"github.com/osama300/aurma-project/internal/tenancy/core"
)

// projectRoutes registers the project-scoped routes. They accept the session or a CLI token.
func (a *API) projectRoutes(with func(authn.Policy, string, string, http.HandlerFunc)) {
	with(anyCred, "GET", "/console/v1/projects", a.listProjects)
	with(anyCred, "POST", "/console/v1/projects", a.createProject)
	with(anyCred, "GET", "/console/v1/projects:checkId", a.checkProjectID)
	with(anyCred, "GET", "/console/v1/projects:suggestId", a.suggestProjectID)
	with(anyCred, "GET", "/console/v1/projects/{project}", a.getProject)
	with(anyCred, "PATCH", "/console/v1/projects/{project}", a.updateProject)
	with(anyCred, "POST", "/console/v1/projects/{project}:delete", a.deleteProject)
	with(anyCred, "POST", "/console/v1/projects/{project}:restore", a.restoreProject)
	with(anyCred, "GET", "/console/v1/projects/{project}/auditLog", a.listAudit)

	a.appRoutes(with)
	a.keyRoutes(with)
	a.serviceAccountRoutes(with)
	a.authRoutes(with)
	a.authEmailRoutes(with)
	a.authProviderRoutes(with)
}

type projectJSON struct {
	Name          string     `json:"name"`
	ProjectID     string     `json:"projectId"`
	ProjectNumber string     `json:"projectNumber"`
	DisplayName   string     `json:"displayName"`
	State         string     `json:"state"`
	ETag          string     `json:"etag"`
	AuthDomain    string     `json:"authDomain"`
	StorageBucket string     `json:"storageBucket"`
	CreateTime    time.Time  `json:"createTime"`
	UpdateTime    time.Time  `json:"updateTime"`
	DeleteTime    *time.Time `json:"deleteTime,omitempty"`
	ExpireTime    *time.Time `json:"expireTime,omitempty"`
}

func toProjectJSON(p core.Project) projectJSON {
	return projectJSON{
		Name: "projects/" + p.ID, ProjectID: p.ID, ProjectNumber: strconv.FormatInt(p.Number, 10),
		DisplayName: p.DisplayName, State: p.State, ETag: p.ETag, AuthDomain: p.AuthDomain,
		StorageBucket: p.StorageBucket, CreateTime: p.CreatedAt, UpdateTime: p.UpdatedAt,
		DeleteTime: p.DeleteTime, ExpireTime: p.ExpireTime,
	}
}

func listOptions(r *http.Request) core.ListOptions {
	q := r.URL.Query()
	size, _ := strconv.Atoi(q.Get("pageSize"))
	deleted, _ := strconv.ParseBool(q.Get("showDeleted"))
	return core.ListOptions{PageSize: size, PageToken: q.Get("pageToken"), ShowDeleted: deleted}
}

func principal(r *http.Request) *authn.Principal { return authn.FromContext(r.Context()) }

func (a *API) listProjects(w http.ResponseWriter, r *http.Request) {
	page, err := a.svc.ListProjects(r.Context(), principal(r), listOptions(r))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out := make([]projectJSON, 0, len(page.Items))
	for _, p := range page.Items {
		out = append(out, toProjectJSON(p))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"projects": out, "nextPageToken": page.NextPageToken})
}

func (a *API) createProject(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ProjectID   string `json:"projectId"`
		DisplayName string `json:"displayName"`
	}
	if err := httpx.DecodeJSON(r, &in, true); err != nil {
		a.fail(w, r, err)
		return
	}
	p, err := a.svc.CreateProject(r.Context(), principal(r), core.NewProject{ID: in.ProjectID, DisplayName: in.DisplayName})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toProjectJSON(p))
}

func (a *API) checkProjectID(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("projectId")
	ok, err := a.svc.ProjectIDAvailable(r.Context(), id)
	if err != nil {
		if apierr.CodeOf(err) == apierr.InvalidArgument {
			httpx.WriteJSON(w, http.StatusOK, map[string]any{"projectId": id, "available": false, "valid": false})
			return
		}
		a.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"projectId": id, "available": ok, "valid": true})
}

func (a *API) suggestProjectID(w http.ResponseWriter, r *http.Request) {
	id, err := a.svc.SuggestProjectID(r.Context(), r.URL.Query().Get("displayName"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"projectId": id})
}

func (a *API) getProject(w http.ResponseWriter, r *http.Request) {
	p, err := a.svc.GetProject(r.Context(), principal(r), r.PathValue("project"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toProjectJSON(p))
}

func (a *API) updateProject(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DisplayName *string `json:"displayName"`
		AuthDomain  *string `json:"authDomain"`
		ETag        string  `json:"etag"`
	}
	if err := httpx.DecodeJSON(r, &in, true); err != nil {
		a.fail(w, r, err)
		return
	}
	p, err := a.svc.UpdateProject(r.Context(), principal(r), r.PathValue("project"), core.ProjectUpdate{
		DisplayName: in.DisplayName, AuthDomain: in.AuthDomain, ETag: in.ETag,
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toProjectJSON(p))
}

// deleteProject requires the caller to type the project id, guarding against a wrong click.
func (a *API) deleteProject(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ConfirmProjectID string `json:"confirmProjectId"`
	}
	if err := httpx.DecodeJSON(r, &in, true); err != nil {
		a.fail(w, r, err)
		return
	}
	ref := r.PathValue("project")
	current, err := a.svc.GetProject(r.Context(), principal(r), ref)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if in.ConfirmProjectID != current.ID {
		a.fail(w, r, apierr.Invalid("confirmProjectId", "Type the project ID to confirm deletion."))
		return
	}
	p, err := a.svc.DeleteProject(r.Context(), principal(r), ref)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toProjectJSON(p))
}

func (a *API) restoreProject(w http.ResponseWriter, r *http.Request) {
	p, err := a.svc.RestoreProject(r.Context(), principal(r), r.PathValue("project"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toProjectJSON(p))
}

type auditJSON struct {
	ID        string          `json:"id"`
	Time      time.Time       `json:"time"`
	ActorType string          `json:"actorType"`
	ActorID   string          `json:"actorId,omitempty"`
	Action    string          `json:"action"`
	Target    string          `json:"target,omitempty"`
	Result    string          `json:"result"`
	IP        string          `json:"ip,omitempty"`
	Details   json.RawMessage `json:"details"`
}

func (a *API) listAudit(w http.ResponseWriter, r *http.Request) {
	page, err := a.svc.ListAudit(r.Context(), principal(r), r.PathValue("project"), listOptions(r))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out := make([]auditJSON, 0, len(page.Items))
	for _, e := range page.Items {
		j := auditJSON{ID: strconv.FormatInt(e.ID, 10), Time: e.At, ActorType: e.ActorType, Action: e.Action, Target: e.Target, Result: e.Result, IP: e.IP, Details: e.Details}
		if e.ActorID != nil {
			j.ActorID = e.ActorID.String()
		}
		out = append(out, j)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"entries": out, "nextPageToken": page.NextPageToken})
}
