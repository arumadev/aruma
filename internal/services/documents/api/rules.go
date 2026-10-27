package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/platform/idtoken"
	"github.com/arumadev/aruma/internal/platform/rules"
	"github.com/arumadev/aruma/internal/services/documents/core"
	"github.com/arumadev/aruma/internal/services/documents/value"
	"github.com/arumadev/aruma/internal/services/documents/wire"
)

// The security rules' binding (F5, ADR-021 §3): the project's cloud.firestore release decides
// every document operation of a caller that is not an admin. Admins bypass the rules, as in
// production, and so does anyone on this machine under documents.dev_open_rules (ADR-025 §8).
// A project without a release has the locked default, which denies everything.
//
//	get     a read: Get, BatchGet, the reads of a transaction. resource is the document read, or
//	        null; request.resource null
//	create  a write whose document does exist after the commit, and either did not before or
//	        has a precondition exists: false
//	update  a write whose document exists after the commit, and either did before or has a
//	        precondition that it does (exists, update_time)
//	delete  a write whose document does not exist after (resource the one before, or null)
//
// A write is judged by its document as stored before the commit (resource, the stored document
// even for a create: f5-rules-request.json rr.create-existing-res-null) and as the
// commit's writes leave it, transforms applied (request.resource): a commit's writes of one
// document are judged once, together. A commit, Write stream batch or transaction commit is
// denied whole when any of its documents is. The rules decide before the preconditions: a create
// of a document that exists is ALREADY_EXISTS when the rules allow the create, PERMISSION_DENIED
// when they do not (f5-rules-methods.json order.*).
//
// An end user may not use BatchWrite, nor begin a read-write transaction, by BeginTransaction or
// a read's new_transaction: production answers PERMISSION_DENIED whatever the rules say
// (f5-rules-batches.json batch-write.ok-ok, f5-rules-transactions.json begin.user-readwrite,
// commit.read-then-allowed). A read-only transaction is theirs (begin.user-readonly).
//
// list (queries, ListDocuments, PartitionQuery) is rules_list.go's, Listen rules_listen.go's.
// ListCollectionIds is not bound: it stays an admin's, or dev_open_rules', alone (requireBypass;
// unrecorded, and no client SDK sends it), never more open than production.

// Rulesets resolves a project's compiled release; tenancy/core implements it. An error is an
// outage of the control plane, never a denial.
type Rulesets interface {
	CompiledRelease(ctx context.Context, project int64, release string) (*rules.Ruleset, error)
}

// FirestoreRelease is the release the rules of the default database are read from: tenancy's
// stored name, cloud.firestore.
const FirestoreRelease = "cloud.firestore"

var (
	// ErrTokenExpired refuses an end user whose ID token has expired (ADR-026 §4); authn's text.
	ErrTokenExpired = wire.NewError(wire.CodeUnauthenticated, "The request's credential has expired.")
	// ErrTokenInvalid refuses an end user whose ID token is not valid: forged, of another
	// project, the user disabled or deleted, their tokens revoked. Production's text for a
	// credential it does not accept (authn.errInvalidCredentials).
	ErrTokenInvalid = wire.NewError(wire.CodeUnauthenticated, "Request had invalid authentication credentials. "+
		"Expected OAuth 2 access token, login cookie or other valid authentication credential. "+
		"See https://developers.google.com/identity/sign-in/web/devconsole-project.")
	// ErrTokenUnchecked refuses an end user whose token could not be verified (an outage); our text.
	ErrTokenUnchecked = wire.NewError(wire.CodeUnavailable, "The credential could not be verified. Retry the request.")
	// errRulesUnavailable is a decision the rules could not make: the control plane or the
	// engine failed, or the process has too many evaluations in progress. Our text; clients
	// retry UNAVAILABLE, and nothing is allowed.
	errRulesUnavailable = wire.NewError(wire.CodeUnavailable, "The security rules could not be evaluated. Retry the request.")
)

// guard is how the rules apply to an admitted request; nil for a caller that bypasses them.
type guard struct {
	auth rules.Value // request.auth: the verified ID token's uid and claims, or null
}

// endUser verifies an end user's ID token for project and returns its claims; nil, without
// error, for a caller with no token. A token the verifier does not accept is ErrTokenInvalid
// (or ErrTokenExpired), and one it could not check ErrTokenUnchecked.
func (s *Service) endUser(ctx context.Context, caller *authn.Caller, project int64) (*idtoken.Claims, error) {
	if caller.Role != authn.RoleEndUser {
		return nil, nil
	}
	if s.o.IDTokens == nil {
		return nil, ErrTokenUnchecked
	}
	claims, err := s.o.IDTokens.Verify(ctx, project, caller.UnverifiedIDToken)
	switch idtoken.Status(err) {
	case "":
		if err != nil {
			s.log.Warn("documents: verifying an ID token", "project_number", project, "error", err)
			return nil, ErrTokenUnchecked
		}
		return claims, nil
	case "expired_token":
		return nil, ErrTokenExpired
	}
	return nil, ErrTokenInvalid
}

// guardOf is the rules' hold on a caller that is not an admin and not let in by
// documents.dev_open_rules: request.auth of its verified token, or null without one.
func guardOf(claims *idtoken.Claims) *guard {
	if claims == nil {
		return &guard{auth: rules.Null()}
	}
	return &guard{auth: rules.Auth(claims.UID, claims.Token)}
}

// requireBypass refuses a request the rules do not bind yet (list, Listen) to a caller they hold.
func (a admitted) requireBypass() error {
	if a.guard != nil {
		return wire.PermissionDenied()
	}
	return nil
}

// refuseHeld refuses what production refuses to a caller the rules hold, whatever they say:
// BatchWrite, and a read-write transaction (the package comment).
func (a admitted) refuseHeld() error {
	if a.guard != nil {
		return wire.PermissionDenied()
	}
	return nil
}

// ruleset resolves the release of a's project, once per request.
func (s *Service) ruleset(ctx context.Context, a admitted) (*rules.Ruleset, error) {
	if s.o.Rulesets == nil {
		return nil, nil // no control plane: the locked default
	}
	rs, err := s.o.Rulesets.CompiledRelease(ctx, a.db.Project, FirestoreRelease)
	if err != nil {
		s.log.Error("documents: the project's rules", "project_number", a.db.Project, "error", err)
		return nil, errRulesUnavailable
	}
	return rs, nil
}

// judge decides one operation under rs: nil when the rules allow it, wire.PermissionDenied when
// they do not, errRulesUnavailable when they cannot decide. A nil rs denies.
func (s *Service) judge(ctx context.Context, a admitted, rs *rules.Ruleset, op ruleOp) error {
	if rs == nil {
		return wire.PermissionDenied()
	}
	match := matchPath(a.db, op.path)
	if len(match) > rs.RequestSegments() {
		return wire.PermissionDenied()
	}
	request := map[string]rules.Value{
		"auth":     a.guard.auth,
		"method":   rules.String(op.method.String()),
		"path":     rules.PathOf(documentPath(a.db, op.path)),
		"time":     rules.Timestamp(op.now),
		"resource": rules.Null(),
	}
	if op.method == rules.MethodCreate || op.method == rules.MethodUpdate {
		request["resource"] = resourceValue(a.db, op.after)
	}
	resource := rules.Null()
	if op.before.Exists() {
		resource = resourceValue(a.db, op.before)
	}
	d, err := rs.Evaluate(ctx, &rules.Request{
		Project:   a.db.Project,
		Method:    op.method,
		Path:      match,
		Variables: map[string]rules.Value{"request": rules.Map(request), "resource": resource},
		Documents: op.lookups,
		Budget:    op.budget,
		Inflight:  s.inflight,
	})
	switch {
	case errors.Is(err, rules.ErrOverloaded):
		return errRulesUnavailable
	case err != nil:
		s.log.Error("documents: the rules could not decide", "project_number", a.db.Project, "method", op.method.String(), "error", err)
		return errRulesUnavailable
	case !d.Allow:
		return wire.PermissionDenied()
	}
	return nil
}

// ruleOp is one operation judge decides.
type ruleOp struct {
	method  rules.Method
	path    core.Path
	before  core.Document // resource: the document as stored, Exists false for none
	after   core.Document // request.resource, for create and update
	now     time.Time     // request.time
	lookups rules.Documents
	budget  *rules.Budget
}

// readBudget is the lookup budget of a request reading documents: each has the operation's
// limit of 10, counting only the documents no earlier one looked up, and the request has 20
// (f5-rules-lookup-limits.json batch-get-r20a denied, batch-get-r1a-r20a allowed;
// f5-rules-lookups.json limit.batch-get-g11x2 denied).
func readBudget() *rules.Budget { return rules.NewReadBudget() }

// allowReads decides a read of docs (get), as read at readTime (zero: the latest), under budget,
// the request's (readBudget). The request is denied whole when any document is.
func (s *Service) allowReads(ctx context.Context, a admitted, docs []core.Document, readTime time.Time, budget *rules.Budget) error {
	if a.guard == nil {
		return nil
	}
	rs, err := s.ruleset(ctx, a)
	if err != nil {
		return err
	}
	l := &lookups{store: s.o.Store, db: a.db, readTime: readTime}
	now := s.now()
	for _, d := range docs {
		op := ruleOp{method: rules.MethodGet, path: d.Path, before: d, now: now, lookups: l, budget: budget}
		if err := s.judge(ctx, a, rs, op); err != nil {
			return err
		}
	}
	return nil
}

// judged returns ctx with the rules' judgement of a's commits (core.WithAuthorizer): each commit
// is denied whole when any of the documents it writes or verifies is.
func (s *Service) judged(ctx context.Context, a admitted) (context.Context, error) {
	if a.guard == nil {
		return ctx, nil
	}
	rs, err := s.ruleset(ctx, a)
	if err != nil {
		return nil, err
	}
	return core.WithAuthorizer(ctx, func(ctx context.Context, proposals []core.Proposal, now time.Time) error {
		l := &lookups{store: s.o.Store, db: a.db, after: make(map[string]core.Document, len(proposals))}
		for _, p := range proposals {
			l.after[p.Path.String()] = p.After
		}
		// Each write has the operation's limit and the commit the request's; the documents the
		// commit writes are free to look up (lookup-limits commit.peer-a-peer-b, write.self-get).
		budget := rules.NewBudget()
		for _, p := range proposals {
			if !p.Verifies {
				budget.Exempt(documentPath(a.db, p.Path))
			}
		}
		for _, p := range proposals {
			op := ruleOp{path: p.Path, before: p.Before, after: p.After, now: now, lookups: l, budget: budget}
			switch {
			case p.Verifies:
				// A verify tells whether the document exists, and its update_time: a get of it
				// (unrecorded, ADR-021 F5 review amendment).
				op.method = rules.MethodGet
			case !p.After.Exists():
				op.method = rules.MethodDelete
			case p.Asserts != nil && *p.Asserts, p.Asserts == nil && p.Before.Exists():
				op.method = rules.MethodUpdate
			default:
				op.method = rules.MethodCreate
			}
			if err := s.judge(ctx, a, rs, op); err != nil {
				return err
			}
		}
		return nil
	}), nil
}

func (s *Service) now() time.Time {
	if s.o.Now != nil {
		return s.o.Now()
	}
	return time.Now()
}

// matchPath is what the match blocks see: databases/{database}/documents and the document's ids.
func matchPath(db core.DB, p core.Path) []rules.Segment {
	return rules.Literals(documentPath(db, p)...)
}

func documentPath(db core.DB, p core.Path) []string {
	return append([]string{"databases", db.ID, "documents"}, p...)
}

// resourceValue is a document as the rules see it: data its fields, __name__ its path from
// databases/, id its own id.
func resourceValue(db core.DB, d core.Document) rules.Value {
	return rules.Map(map[string]rules.Value{
		"data":     ruleValue(d.Fields),
		"__name__": rules.PathOf(documentPath(db, d.Path)),
		"id":       rules.String(d.Path.ID()),
	})
}

// ruleValue converts a stored value. A map keeps its stored order, which values() and keys()
// show (deployed-firestore.json orderNote). A reference is the path of the document it names,
// from databases/ (provisional: no recording compares one).
func ruleValue(v value.Value) rules.Value {
	switch v.Kind() {
	case value.KindBoolean:
		return rules.Bool(v.BoolValue())
	case value.KindInteger:
		return rules.Int(v.IntValue())
	case value.KindDouble:
		return rules.Float(v.DoubleValue())
	case value.KindTimestamp:
		sec, nanos := v.TimestampValue()
		return rules.Timestamp(time.Unix(sec, int64(nanos)))
	case value.KindString:
		return rules.String(v.StringValue())
	case value.KindBytes:
		return rules.Bytes(v.BytesValue())
	case value.KindReference:
		name := v.StringValue()
		if _, rest, ok := strings.Cut(name, "/databases/"); ok {
			return rules.PathOf(append([]string{"databases"}, strings.Split(rest, "/")...))
		}
		return rules.String(name)
	case value.KindGeoPoint:
		return rules.LatLng(v.GeoPointValue())
	case value.KindArray:
		el := v.Elements()
		out := make([]rules.Value, len(el))
		for i, e := range el {
			out[i] = ruleValue(e)
		}
		return rules.List(out)
	case value.KindMap:
		fs := v.Fields()
		keys, vals := make([]string, len(fs)), make([]rules.Value, len(fs))
		for i, f := range fs {
			keys[i], vals[i] = f.Name, ruleValue(f.Value)
		}
		return rules.MapInOrder(keys, vals)
	}
	return rules.Null()
}

// lookups is get(), exists(), getAfter() and existsAfter() for one request: reads at the
// request's read point (readTime; zero, the latest), and after, the documents as the request's
// writes leave them. A lookup of another project than the request's is refused as an outage:
// the engine never asks one (rules.Request.Project), and none must ever be answered.
type lookups struct {
	store    core.Store
	db       core.DB
	readTime time.Time
	after    map[string]core.Document // by path, for getAfter and existsAfter
}

// Get implements rules.Documents.
func (l *lookups) Get(ctx context.Context, project int64, path []string, after bool) (rules.Value, bool, error) {
	if project != l.db.Project {
		return rules.Value{}, false, fmt.Errorf("documents: a lookup of project %d in a request of project %d", project, l.db.Project)
	}
	p, ok := lookupPath(l.db, path)
	if !ok {
		return rules.Value{}, false, rules.ErrNoDocuments
	}
	if after {
		if d, ok := l.after[p.String()]; ok {
			if !d.Exists() {
				return rules.Value{}, false, nil
			}
			return resourceValue(l.db, d), true, nil
		}
	}
	d, _, err := l.store.Get(ctx, l.db, p, core.ReadOptions{ReadTime: l.readTime})
	if ce, ok := errors.AsType[*core.Error](err); ok && ce.Code == core.NotFound {
		return rules.Value{}, false, nil
	}
	if err != nil {
		return rules.Value{}, false, err
	}
	return resourceValue(l.db, d), true, nil
}

// lookupPath is the document a lookup names, databases/{db}/documents/..., its ids checked as a
// request's are; false for one Store may not be asked.
func lookupPath(db core.DB, path []string) (core.Path, bool) {
	if len(path) < 5 || len(path)%2 == 0 || path[0] != "databases" || path[1] != db.ID || path[2] != "documents" {
		return nil, false
	}
	p := core.Path(path[3:])
	for i, id := range p {
		if wire.CheckID(id, i%2 == 1) != nil {
			return nil, false
		}
	}
	return p, true
}

// Lookups is the Documents Storage's rules read (firestore.get, firestore.exists): the default
// database of the project the request is for, at the latest commit. The project is the
// request's, as the engine passes it; no lookup reads another.
func Lookups(store core.Store) rules.Documents { return storageLookups{store} }

type storageLookups struct{ store core.Store }

// Get implements rules.Documents.
func (s storageLookups) Get(ctx context.Context, project int64, path []string, after bool) (rules.Value, bool, error) {
	l := &lookups{store: s.store, db: core.DB{Project: project, ID: core.DefaultDatabase}}
	return l.Get(ctx, project, path, false)
}
