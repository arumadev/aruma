// Package mgmt is the subset of the Firebase Management REST API (v1beta1) that the firebase
// CLI, the FlutterFire CLI and the Admin SDKs need (M-01..M-18), mounted at /v1beta1/, and with
// the Realtime Database on, the part of its management API (v1beta) that firebase-tools reads
// (rtdb.go), mounted at /v1beta/.
//
// Callers authenticate with a personal CLI token or a service account access token; a
// service account sees only its own project. Errors are google.rpc.Status.
package mgmt

import (
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/arumadev/aruma/internal/platform/apierr"
	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/platform/httpx"
	"github.com/arumadev/aruma/internal/platform/rules"
	"github.com/arumadev/aruma/internal/tenancy/core"
)

// API is the Management API.
type API struct {
	svc    *core.Service
	auth   *authn.Authenticator
	log    *slog.Logger
	opts   Options
	router *httpx.Router
}

// Options configure the API.
type Options struct {
	// RTDB is set when the Realtime Database is served: projects then name their default
	// database, and its management API answers.
	RTDB bool
	// Storage is set when Cloud Storage is served: defaultBucket and the firebaserules API
	// answer for it (S5-A, ADR-022 §15).
	Storage bool
	// Documents is set when the Document Database is served: the firebaserules API answers
	// for its cloud.firestore release, Storage on or off (firebase deploy --only
	// firestore:rules).
	Documents bool
	// RulesLimits are the rules engine's, for the firebaserules API: the same limits as every
	// ruleset. :test runs no test case, so it evaluates nothing and needs no rules.Inflight.
	RulesLimits rules.Limits
}

var policy = authn.Policy{AccessToken: true, ServiceToken: true}

// New returns the Management API.
func New(svc *core.Service, auth *authn.Authenticator, logger *slog.Logger, opts Options) *API {
	a := &API{svc: svc, auth: auth, log: logger, opts: opts}
	a.router = a.routes()
	return a
}

// Handler returns the routed API.
func (a *API) Handler() http.Handler { return a.router }

// Routes lists the registered routes, for the authentication test.
func (a *API) Routes() []httpx.RouteInfo { return a.router.Routes() }

func (a *API) routes() *httpx.Router {
	r := &httpx.Router{
		NotFound: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			apierr.Write(w, apierr.New(apierr.NotFound, "Requested entity was not found."))
		}),
		MethodNotAllowed: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			apierr.Write(w, apierr.New(apierr.NotFound, "Requested entity was not found."))
		}),
	}
	h := func(method, pattern string, fn http.HandlerFunc) {
		r.Handle(method, "/v1beta1"+pattern, a.auth.Require(policy, fn))
	}
	h("GET", "/projects", a.listProjects)                            // M-01
	h("GET", "/projects/{project}", a.getProject)                    // M-02
	h("PATCH", "/projects/{project}", a.patchProject)                // M-03
	h("GET", "/availableProjects", a.availableProjects)              // M-04
	h("GET", "/projects/{project}:searchApps", a.searchApps)         // M-05
	h("GET", "/projects/{project}/adminSdkConfig", a.adminSdkConfig) // M-18
	h("GET", "/operations/{name...}", a.getOperation)                // M-17

	for _, platform := range []string{core.PlatformWeb, core.PlatformIOS, core.PlatformAndroid} {
		c := "/projects/{project}/" + collection(platform)
		h("GET", c, a.listApps(platform))                       // M-06, M-09, M-12
		h("POST", c, a.createApp(platform))                     // M-06, M-09, M-12
		h("GET", c+"/{app}", a.getApp(platform))                // M-07, M-10, M-13
		h("PATCH", c+"/{app}", a.patchApp(platform))            // M-07, M-10, M-13
		h("GET", c+"/{app}/config", a.getConfig(platform))      // M-08, M-11, M-14
		h("POST", c+"/{app}:remove", a.removeApp(platform))     // M-16
		h("POST", c+"/{app}:undelete", a.undeleteApp(platform)) // M-16
	}
	c := "/projects/{project}/androidApps/{app}/sha"
	h("GET", c, a.listSha)               // M-15
	h("POST", c, a.createSha)            // M-15
	h("DELETE", c+"/{sha}", a.deleteSha) // M-15

	if a.opts.RTDB {
		c := "/v1beta/projects/{project}/locations/{location}/instances"
		r.Handle("GET", c, a.auth.Require(policy, http.HandlerFunc(a.listDatabaseInstances)))
		r.Handle("GET", c+"/{instance}", a.auth.Require(policy, http.HandlerFunc(a.getDatabaseInstance)))
	}
	// defaultBucket, buckets and Service Usage (storage.go) for the firebase CLI (ADR-022 §15),
	// answered with Storage off too, so the CLI learns Storage is not set up; and firebaserules
	// v1 (rules.go) with Storage or the Document Database on.
	a.storageRoutes(r)
	if a.opts.Storage || a.opts.Documents {
		a.rulesRoutes(r)
	}
	return r
}

func (a *API) fail(w http.ResponseWriter, r *http.Request, err error) {
	httpx.WriteError(w, r, a.log, err)
}

func principal(r *http.Request) *authn.Principal { return authn.FromContext(r.Context()) }

// param reads a query parameter in camelCase or snake_case; Google's gRPC transcoding accepts
// both and the Node Admin SDK sends snake_case.
func param(r *http.Request, camel, snake string) string {
	q := r.URL.Query()
	if v := q.Get(camel); v != "" {
		return v
	}
	return q.Get(snake)
}

func listOptions(r *http.Request) (core.ListOptions, error) {
	var o core.ListOptions
	if s := param(r, "pageSize", "page_size"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			return o, apierr.Invalid("pageSize", "pageSize must be a non-negative integer.")
		}
		o.PageSize = n
	}
	o.PageToken = param(r, "pageToken", "page_token")
	o.ShowDeleted, _ = strconv.ParseBool(param(r, "showDeleted", "show_deleted"))
	return o, nil
}

// updateMask parses the mask into canonical camelCase field names.
func updateMask(r *http.Request) []string {
	raw := param(r, "updateMask", "update_mask")
	if raw == "" {
		return nil
	}
	var out []string
	for _, f := range strings.Split(raw, ",") {
		f = strings.TrimSpace(f)
		if f == "display_name" {
			f = "displayName"
		}
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

func checkMask(mask []string, allowed ...string) error {
	for _, f := range mask {
		ok := false
		for _, a := range allowed {
			ok = ok || f == a
		}
		if !ok {
			return apierr.Invalid("updateMask", "Field "+f+" cannot be updated.")
		}
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// Operation names carry the resource they describe, so operations need no storage: getting an
// operation re-reads its resource with the caller's own credentials.
type opRef struct {
	Kind string `json:"k"` // "app", or "empty" for an immediate removal
	Name string `json:"n"`
}

func operationName(ref opRef) string {
	b, _ := json.Marshal(ref)
	return "operations/aruma/" + base64.RawURLEncoding.EncodeToString(b)
}

func parseOperation(name string) (opRef, bool) {
	var ref opRef
	enc, ok := strings.CutPrefix(name, "aruma/")
	if !ok {
		return ref, false
	}
	b, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil || json.Unmarshal(b, &ref) != nil {
		return ref, false
	}
	return ref, true
}

func withType(typ string, resource any) map[string]any {
	b, _ := json.Marshal(resource)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if m == nil {
		m = map[string]any{}
	}
	m["@type"] = typ
	return m
}
