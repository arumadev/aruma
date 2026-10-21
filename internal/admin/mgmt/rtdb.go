package mgmt

import (
	"net/http"
	"strconv"

	"github.com/arumadev/aruma/internal/platform/apierr"
	"github.com/arumadev/aruma/internal/platform/httpx"
	"github.com/arumadev/aruma/internal/tenancy/core"
)

// The Realtime Database management API (firebasedatabase.googleapis.com/v1beta), the part
// firebase-tools reads before any database command or deploy --only database: the instance a
// project's resources name (realtimeDatabaseInstance), and that instance's details, whose
// databaseUrl is where the CLI then sends REST (firebase-tools management/database.ts
// getDatabaseInstanceDetails and listDatabaseInstances). The CLI reaches it through
// FIREBASE_RTDB_MANAGEMENT_URL. Each project has its default database only (ADR-018), which
// exists as soon as the project does; creating or deleting instances is not served.

// rtdbLocation is the location every database reports. Aruma has one; the CLI only parses it.
const rtdbLocation = "us-central1"

type databaseInstance struct {
	Name        string `json:"name"`
	Project     string `json:"project"`
	DatabaseURL string `json:"databaseUrl"`
	Type        string `json:"type"`
	State       string `json:"state"`
}

func toInstance(p core.Project) databaseInstance {
	return databaseInstance{
		Name:        "projects/" + strconv.FormatInt(p.Number, 10) + "/locations/" + rtdbLocation + "/instances/" + p.ID + rtdbSuffix,
		Project:     "projects/" + strconv.FormatInt(p.Number, 10),
		DatabaseURL: p.DatabaseURL,
		Type:        "DEFAULT_DATABASE",
		State:       "ACTIVE",
	}
}

// rtdbSuffix ends the name of a project's default database (ADR-018).
const rtdbSuffix = "-default-rtdb"

// activeProject resolves the request's project, as the caller may see it.
func (a *API) activeProject(r *http.Request) (core.Project, error) {
	p, err := a.svc.GetProject(r.Context(), principal(r), r.PathValue("project"))
	if err == nil && p.State != "ACTIVE" {
		err = apierr.New(apierr.NotFound, "Requested entity was not found.")
	}
	return p, err
}

// locationOK accepts "-" (any) and the one location there is.
func locationOK(loc string) bool { return loc == "-" || loc == rtdbLocation }

func (a *API) getDatabaseInstance(w http.ResponseWriter, r *http.Request) {
	p, err := a.activeProject(r)
	if err == nil && (!locationOK(r.PathValue("location")) || r.PathValue("instance") != p.ID+rtdbSuffix) {
		err = apierr.New(apierr.NotFound, "Requested entity was not found.")
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toInstance(p))
}

func (a *API) listDatabaseInstances(w http.ResponseWriter, r *http.Request) {
	p, err := a.activeProject(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out := []databaseInstance{}
	if locationOK(r.PathValue("location")) {
		out = append(out, toInstance(p))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"instances": out})
}
