// Package console serves the dashboard's Document Database screens under
// /console/v1/projects/{project}/documents (ADR-030): the data browser (collections, documents
// with their typed fields, create, edit, delete and recursive delete), the rules editor, the
// composite indexes, the single-field exemptions, the TTL policies and usage.
//
// The console API mounts these routes and authenticates them (ADR-010 S-4), with the CSRF header
// on writes (S-5). They act on the '(default)' database of a project the caller owns, as an
// admin the rules do not restrict, as the Firebase console does. Every operation goes through
// the service layer (package api) with the caller's account as an already authenticated admin
// (api.Call.Caller), so the values, names, limits, writes, queries and index administration are
// the ones the Firestore APIs serve, and their errors keep production's texts. Values travel in
// Firestore's REST form ({"stringValue": …}), so the dashboard shows and edits each type
// exactly. Every write is audited; the rules are saved and published by tenancy, which audits
// them itself. No route reads a body over 1 MiB.
//
// The routes live in one file per area: data.go, rules.go, indexes.go.
package console

import (
	"context"
	"errors"
	"net/http"

	"github.com/arumadev/aruma/internal/platform/apierr"
	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/platform/consolemount"
	"github.com/arumadev/aruma/internal/platform/httpx"
	"github.com/arumadev/aruma/internal/platform/rules"
	"github.com/arumadev/aruma/internal/services/documents/api"
	"github.com/arumadev/aruma/internal/services/documents/core"
	"github.com/arumadev/aruma/internal/services/documents/value"
	"github.com/arumadev/aruma/internal/services/documents/wire"
	tcore "github.com/arumadev/aruma/internal/tenancy/core"
)

// Rules is the control plane's rulesets and releases, as the rules editor needs them; tenancy
// implements it.
type Rules interface {
	// CompileRules compiles a source under the configured limits, in a turn of the process's
	// compilations, as every compilation is (CreateRuleset's included).
	CompileRules(ctx context.Context, svc rules.Service, f rules.File) (*rules.Ruleset, rules.Issues, error)
	CreateRuleset(ctx context.Context, p *authn.Principal, project string, svc rules.Service, files []rules.File) (tcore.Ruleset, rules.Issues, error)
	GetRuleset(ctx context.Context, p *authn.Principal, project, name string) (tcore.Ruleset, error)
	ListRulesets(ctx context.Context, p *authn.Principal, project string, opt tcore.ListOptions) (tcore.Page[tcore.Ruleset], error)
	GetRelease(ctx context.Context, p *authn.Principal, project, release string) (tcore.Release, error)
	PublishRelease(ctx context.Context, p *authn.Principal, project, release, ruleset string) (tcore.Release, error)
}

// Options configure the routes.
type Options struct {
	// Rules saves, reads and publishes the Firestore rules.
	Rules Rules
	// Audit records the dashboard's writes.
	Audit func(ctx context.Context, p *authn.Principal, project int64, action, target string, details map[string]any) error
}

// anyCred accepts the owner's session or a CLI token, like the console's other project routes.
var anyCred = authn.Policy{Session: true, AccessToken: true}

// maxBody is the body cap of every route here, the console's own (httpx.MaxBodyBytes, 1 MiB),
// enforced again by each route so it holds whatever mounts them. A document near the 1 MiB
// limit is larger than that in JSON: the dashboard cannot write one (ADR-030 §4).
const maxBody = httpx.MaxBodyBytes

// databaseID is the one database the dashboard shows.
const databaseID = "(default)"

// Routes returns the mount that registers the Document Database screens' routes with the console
// API. svc is nil while the Document Database is disabled: the routes still exist, the info
// route says so, and the others answer FAILED_PRECONDITION.
func Routes(svc *api.Service, opts Options) func(*consolemount.Mount) {
	return func(m *consolemount.Mount) {
		h := &handlers{svc: svc, opts: opts, m: m}
		h.handle("GET", base, h.getInfo)
		h.handle("GET", base+"/usage", h.getUsage)
		h.dataRoutes()
		h.rulesRoutes()
		h.indexRoutes()
	}
}

// base is the path every route here starts with.
const base = "/console/v1/projects/{project}/documents"

type handlers struct {
	svc  *api.Service
	opts Options
	m    *consolemount.Mount
}

// handle registers a route behind the console's authentication, with the body cap.
func (h *handlers) handle(method, pattern string, fn http.HandlerFunc) {
	h.m.Handle(anyCred, method, pattern, func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > maxBody {
			h.m.Fail(w, r, apierr.PayloadTooLarge())
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		fn(w, r)
	})
}

// errDisabled answers the routes of a server that does not serve the Document Database.
var errDisabled = apierr.New(apierr.FailedPrecondition, "The Document Database is not enabled on this server (documents.enabled).")

// target is what a request acts on: the project the caller owns, and who acts.
type target struct {
	project consolemount.ProjectRef
	caller  *authn.Principal
	// call is the caller as the service layer sees it: an admin, from the dashboard's account.
	call api.Call
}

// target resolves the {project} of the request for the signed-in owner. It fails, ready for
// Fail, when the project is not the caller's or the Document Database is disabled.
func (h *handlers) target(r *http.Request) (target, error) {
	p, err := h.m.Project(r)
	if err != nil {
		return target{}, err
	}
	if h.svc == nil {
		return target{}, errDisabled
	}
	caller := authn.FromContext(r.Context())
	if caller == nil || caller.Kind != authn.KindAccount {
		return target{}, apierr.New(apierr.PermissionDenied, "The dashboard's Document Database routes take an account's credentials.")
	}
	return target{project: p, caller: caller, call: api.Call{
		Transport: authn.TransportOf(r),
		Caller:    &authn.Caller{Role: authn.RoleAdmin, Principal: caller},
	}}, nil
}

// database is the database's name, projects/{p}/databases/(default).
func (t target) database() string { return "projects/" + t.project.ID + "/databases/" + databaseID }

// root is the name of the database's documents root.
func (t target) root() string { return t.database() + "/documents" }

// audit records a write that took effect. A log that cannot record it is the server's fault,
// and the caller learns of it: the change stands, and the message says so.
func (h *handlers) audit(r *http.Request, t target, action, name string, details map[string]any) error {
	if err := h.opts.Audit(context.WithoutCancel(r.Context()), t.caller, t.project.Number, action, name, details); err != nil {
		return apierr.Wrap(err, apierr.Internal, "The change was made, but the audit log did not record it.")
	}
	return nil
}

// docError renders the service layer's error as google.rpc.Status with its code and message,
// which are production's (the dashboard shows them as the Firebase console would).
func docError(err error) error {
	if e, ok := errors.AsType[*wire.Error](err); ok {
		if e.Code == wire.CodeInternal {
			return apierr.Wrap(err, apierr.Internal, "%s", e.Message)
		}
		return apierr.New(apierr.Code(e.Code), "%s", e.Message)
	}
	return err
}

func (h *handlers) fail(w http.ResponseWriter, r *http.Request, err error) {
	h.m.Fail(w, r, docError(err))
}

// Page sizes of the data browser. defaultPage is a guess until R7 records the Firebase
// console's (ADR-030 §3).
const (
	defaultPage = 50
	maxPage     = core.MaxPageSize
)

// getInfo describes the project's database: whether the server serves it, its name, and the
// limits the dashboard's reads and edits must respect.
func (h *handlers) getInfo(w http.ResponseWriter, r *http.Request) {
	p, err := h.m.Project(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	out := map[string]any{
		"enabled":    h.svc != nil,
		"databaseId": databaseID,
		"name":       "projects/" + p.ID + "/databases/" + databaseID,
	}
	if h.svc != nil {
		out["limits"] = map[string]any{
			"pageSize":            defaultPage,
			"maxPageSize":         maxPage,
			"documentBytes":       value.MaxDocumentSize,
			"fieldDepth":          value.MaxDepth,
			"requestBytes":        maxBody,
			"recursiveDeleteDocs": maxRecursiveDelete,
		}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// getUsage answers what the server counts for the project's database. It keeps no per-project
// counters of reads, writes or deletes today (its metrics are process-wide, by method), so the
// answer says so rather than show another project's traffic (ADR-030 §8).
func (h *handlers) getUsage(w http.ResponseWriter, r *http.Request) {
	if _, err := h.target(r); err != nil {
		h.m.Fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"available": false,
		"counters":  map[string]any{},
		"note":      "This server does not count reads, writes or deletes per project yet.",
	})
}
