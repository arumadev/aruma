// Package console serves the dashboard's Storage screens under
// /console/v1/projects/{project}/storage (ADR-022 §15): browsing by prefix, upload sessions the
// dashboard issues (whose chunks go to /v0, without the cookie), downloads through media in its
// Console mode, deletion, metadata, tokens, CORS, usage, and the Storage rules through tenancy.
// Every write is audited.
//
// The console API mounts these routes and authenticates them (ADR-010 S-4), with the CSRF
// header on writes (S-5); they act on the default bucket of a project the caller owns, as an
// admin the rules do not restrict, with the caller's account bound to every upload session they
// issue. No route reads a body over 1 MiB: the bytes of an upload go to /v0. The routes live in
// one file per area: objects.go, uploads.go, settings.go, rules.go.
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
	"github.com/arumadev/aruma/internal/services/storage/core"
	tcore "github.com/arumadev/aruma/internal/tenancy/core"
)

// Rules is the control plane's rulesets and releases, as the rules editor needs them; tenancy
// implements it.
type Rules interface {
	CreateRuleset(ctx context.Context, p *authn.Principal, project string, svc rules.Service, files []rules.File) (tcore.Ruleset, rules.Issues, error)
	GetRuleset(ctx context.Context, p *authn.Principal, project, name string) (tcore.Ruleset, error)
	GetRelease(ctx context.Context, p *authn.Principal, project, release string) (tcore.Release, error)
	PublishRelease(ctx context.Context, p *authn.Principal, project, release, ruleset string) (tcore.Release, error)
}

// Options configure the routes.
type Options struct {
	// PublicURL is the server's address without a trailing slash: the /v0 session URLs the
	// dashboard's uploads go to are absolute from it.
	PublicURL string
	// ProjectDomain names a project's bucket, <project_id>.<project_domain> (ADR-022 §2).
	ProjectDomain string
	// Rules saves, reads and publishes the Storage rules.
	Rules Rules
	// Audit records the dashboard's writes.
	Audit func(ctx context.Context, p *authn.Principal, project int64, action, target string, details map[string]any) error
}

// anyCred accepts the owner's session or a CLI token, like the console's other project routes.
// Both are an account's, which the upload sessions bind (core.Principal.ConsoleAccount).
var anyCred = authn.Policy{Session: true, AccessToken: true}

// maxBody is the body cap of every route here, the console's own (httpx.MaxBodyBytes, 1 MiB),
// enforced again by each route so it holds whatever mounts them: the dashboard's uploads are
// small JSON that issues a session, and their bytes go to /v0 (ADR-022 §15).
const maxBody = httpx.MaxBodyBytes

// Routes returns the mount that registers the Storage screens' routes with the console API.
// svc is nil while Storage is disabled: the routes still exist, the info route says so, and
// the others answer FAILED_PRECONDITION.
func Routes(svc *core.Service, opts Options) func(*consolemount.Mount) {
	return func(m *consolemount.Mount) {
		h := &handlers{svc: svc, opts: opts, m: m}
		h.handle("GET", "/console/v1/projects/{project}/storage", h.getStorage)
		h.objectRoutes()
		h.uploadRoutes()
		h.settingsRoutes()
		h.rulesRoutes()
	}
}

type handlers struct {
	svc  *core.Service
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

// errDisabled answers the routes of a server that does not serve Storage.
var errDisabled = apierr.New(apierr.FailedPrecondition, "Cloud Storage is not enabled on this server (storage.enabled).")

// target is what a request acts on: the project the caller owns, its bucket, and who acts.
type target struct {
	project consolemount.ProjectRef
	bucket  core.Bucket
	caller  *authn.Principal
	// as is the caller as Storage sees it: an admin, from the dashboard's account.
	as core.Principal
}

// target resolves the {project} of the request for the signed-in owner to its bucket. It fails,
// ready for Fail, when the project is not the caller's, Storage is disabled, or the bucket is
// not active.
func (h *handlers) target(r *http.Request) (target, error) {
	p, err := h.m.Project(r)
	if err != nil {
		return target{}, err
	}
	if h.svc == nil {
		return target{}, errDisabled
	}
	b, err := h.svc.Bucket(r.Context(), h.bucketName(p))
	if err != nil {
		return target{}, storageError(err)
	}
	if b.Project != p.Number {
		return target{}, apierr.New(apierr.NotFound, "Requested entity was not found.")
	}
	caller := authn.FromContext(r.Context())
	if caller == nil || caller.Kind != authn.KindAccount {
		return target{}, apierr.New(apierr.PermissionDenied, "The dashboard's Storage routes take an account's credentials.")
	}
	return target{project: p, bucket: b, caller: caller,
		as: core.Principal{Kind: core.PrincipalAdmin, ConsoleAccount: caller.AccountID.String()}}, nil
}

func (h *handlers) bucketName(p consolemount.ProjectRef) string {
	return p.ID + "." + h.opts.ProjectDomain
}

// audit records a write that took effect. A log that cannot record it is the server's fault,
// and the caller learns of it: the change stands, and the message says so.
func (h *handlers) audit(r *http.Request, t target, action, name string, details map[string]any) error {
	if err := h.opts.Audit(context.WithoutCancel(r.Context()), t.caller, t.project.Number, action, name, details); err != nil {
		return apierr.Wrap(err, apierr.Internal, "The change was made, but the audit log did not record it.")
	}
	return nil
}

// storageError renders a Storage error as google.rpc.Status, with the dashboard's words.
func storageError(err error) error {
	if invalid, ok := errors.AsType[*core.InvalidError](err); ok {
		return apierr.New(apierr.InvalidArgument, "%s", invalid.Message)
	}
	e, ok := errors.AsType[*core.Error](err)
	if !ok {
		return err
	}
	switch e {
	case core.ErrBucketNotFound:
		return apierr.New(apierr.NotFound, "The project has no active bucket.")
	case core.ErrNotFound:
		return apierr.New(apierr.NotFound, "No such object.")
	case core.ErrPermissionDenied:
		return apierr.New(apierr.PermissionDenied, "Permission denied.")
	case core.ErrQuotaExceeded:
		return apierr.New(apierr.ResourceExhausted, "The project's storage quota is used up.")
	case core.ErrTooLarge:
		return apierr.New(apierr.InvalidArgument, "The object is larger than this server accepts (storage.limits.object_bytes).")
	case core.ErrTooManyUploads:
		return apierr.New(apierr.ResourceExhausted, "Too many upload sessions are open. Finish or cancel some first.")
	case core.ErrInvalidPageToken:
		return apierr.Invalid("pageToken", "The page token is not valid for this listing.")
	case core.ErrPreconditionFailed:
		return apierr.New(apierr.FailedPrecondition, "The object changed meanwhile.")
	case core.ErrOverloaded, core.ErrUnavailable:
		return apierr.Wrap(err, apierr.Unavailable, "Storage is unavailable. Try again later.")
	case core.ErrInvalidArgument:
		return apierr.New(apierr.InvalidArgument, "The request is not valid.")
	}
	return err
}

func (h *handlers) fail(w http.ResponseWriter, r *http.Request, err error) {
	h.m.Fail(w, r, storageError(err))
}

// getStorage describes the project's bucket: its name, whether the server serves Storage, and
// the limits the dashboard's uploads and edits must respect.
func (h *handlers) getStorage(w http.ResponseWriter, r *http.Request) {
	p, err := h.m.Project(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	out := map[string]any{"bucket": h.bucketName(p), "enabled": h.svc != nil}
	if h.svc != nil {
		lim := h.svc.Limits()
		out["limits"] = map[string]any{
			"objectBytes":          lim.ObjectBytes,
			"projectBytes":         lim.ProjectBytes,
			"metadataBytes":        lim.MetadataBytes,
			"headerFieldBytes":     lim.HeaderFieldBytes,
			"listMaxResults":       lim.ListMaxResults,
			"uploadSessionSeconds": int64(lim.ConsoleSessionTTL.Seconds()),
			"requestBytes":         maxBody,
		}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}
