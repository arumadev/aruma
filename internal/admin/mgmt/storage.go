package mgmt

import (
	"net/http"
	"strconv"

	"github.com/arumadev/aruma/internal/platform/apierr"
	"github.com/arumadev/aruma/internal/platform/httpx"
	"github.com/arumadev/aruma/internal/tenancy/core"
)

// What firebase deploy --only storage and firebase init storage ask before the rules (C §1.3,
// §1.4, ADR-022 §15): Service Usage, which they reach through FIREBASE_SERVICE_USAGE_URL to
// check that the API named by FIREBASE_FIREBASESTORAGE_URL's host is on, and the project's
// default bucket from the Firebase Storage API at that origin. Each project has one bucket,
// <project>.<project_domain> (ADR-022 §2), the storageBucket of its config files.
//
// The routes answer with Storage off too, in the JSON the CLI reads: Service Usage still says
// ENABLED, since the CLI would otherwise enable and poll for two minutes before it gives up;
// defaultBucket is 404, on which both commands print "Firebase Storage has not been set up on
// project" and stop before any rules request (gcp/storage.ts:214-219); and the bucket list is
// empty, which ext:install reads as Storage not set up.

// storageLocation is where every bucket reports it is. Aruma has one place; the CLI does not read
// it (C §1.3).
const storageLocation = "US-CENTRAL1"

// storageRoutes registers what the firebase CLI asks of Storage besides the rules (ADR-022 §15):
// GET /v1alpha/projects/{p}/defaultBucket, GET /v1beta/projects/{p}/buckets, and Service
// Usage's GET /v1/projects/{p}/services/{name} and :enable.
func (a *API) storageRoutes(r *httpx.Router) {
	h := func(method, pattern string, fn http.HandlerFunc) {
		r.Handle(method, pattern, a.auth.Require(policy, fn))
	}
	h("GET", "/v1alpha/projects/{project}/defaultBucket", a.defaultBucket)
	h("GET", "/v1beta/projects/{project}/buckets", a.listBuckets)
	h("GET", "/v1/projects/{project}/services/{service}", a.getService)
	h("POST", "/v1/projects/{project}/services/{service}:enable", a.enableService)
}

type bucketRef struct {
	Name string `json:"name"`
}

// defaultBucket answers firebase-tools' getDefaultBucket, which takes the last segment of
// bucket.name; the bucket object is always there, since without it the CLI fails with a
// TypeError (C §1.3).
func (a *API) defaultBucket(w http.ResponseWriter, r *http.Request) {
	p, err := a.activeProject(r)
	if err == nil && !a.opts.Storage {
		err = apierr.New(apierr.NotFound, "Cloud Storage is not enabled on this server.")
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, struct {
		Name     string    `json:"name"`
		Location string    `json:"location"`
		Bucket   bucketRef `json:"bucket"`
	}{
		Name:     "projects/" + p.ID + "/defaultBucket",
		Location: storageLocation,
		Bucket:   bucketRef{Name: "projects/" + p.ID + "/buckets/" + p.StorageBucket},
	})
}

// listBuckets is the Firebase Storage API's bucket list, which firebase ext:install reads to
// see whether Storage is set up. Its names carry the project number, as firebase-tools expects
// (extensions/provisioningHelper.ts:127-129). With Storage off it is empty, {} as proto3 writes it.
func (a *API) listBuckets(w http.ResponseWriter, r *http.Request) {
	p, err := a.activeProject(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if !a.opts.Storage {
		httpx.WriteJSON(w, http.StatusOK, struct{}{})
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"buckets": []bucketRef{{Name: "projects/" + strconv.FormatInt(p.Number, 10) + "/buckets/" + p.StorageBucket}},
	})
}

// service is a Service Usage service. Every one is ENABLED: what Aruma serves needs no enabling,
// and the CLI asks for the host it was pointed at, which may be Aruma's own (C §1.4).
type service struct {
	Name   string `json:"name"`
	Parent string `json:"parent"`
	State  string `json:"state"`
}

func toService(p core.Project, name string) service {
	parent := "projects/" + strconv.FormatInt(p.Number, 10)
	return service{Name: parent + "/services/" + name, Parent: parent, State: "ENABLED"}
}

func (a *API) getService(w http.ResponseWriter, r *http.Request) {
	p, err := a.activeProject(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toService(p, r.PathValue("service")))
}

// enableService answers with a finished operation; the CLI reads nothing of it, and only sends
// it when getService did not say ENABLED.
func (a *API) enableService(w http.ResponseWriter, r *http.Request) {
	p, err := a.activeProject(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"name": "operations/noop.DONE_OPERATION",
		"done": true,
		"response": map[string]any{
			"@type":   "type.googleapis.com/google.api.serviceusage.v1.EnableServiceResponse",
			"service": toService(p, r.PathValue("service")),
		},
	})
}
