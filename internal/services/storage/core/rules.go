package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/arumadev/aruma/internal/platform/rules"
	tcore "github.com/arumadev/aruma/internal/tenancy/core"
)

// The rules' binding (ADR-022 §8): one evaluation per operation, admins bypassing it as in
// Firebase (ADR-021 §7). The operations build their ruleOp so:
//
//	get     Method get, Name; Resource the object stored, or nil
//	list    Method list, List, Name the prefix
//	create  Method create, Name; Resource the object stored (an upload over it is a create too),
//	        or nil; Incoming the new object with its actual size and hashes; at finalize, with
//	        the principal the session's start bound
//	update  Method update, Name; Resource the object stored; Patch the change. An Incoming the
//	        caller sets gives the content fields only: the versions are always Resource's
//	delete  Method delete, Name; Resource the object stored, or nil
//
// The rules decide before the object's existence does: a denial is ErrPermissionDenied even
// where the object is missing, so the answer never says whether a name exists (§8).
//
// Every operation resolves the ruleset with prepareRules before it begins any transaction, and
// decides with the context it returns: authorize never reads the control plane itself, so a
// decision under a row lock takes no connection from the pool.

// authorize decides an operation (ADR-022 §8): admins bypass the rules; everyone else answers
// to the project's Storage rules, as prepareRules resolved them. A denial is
// ErrPermissionDenied; an evaluation the process cannot afford now is ErrOverloaded, and a
// failure of the control plane or the engine ErrUnavailable: both are 503 on the surfaces,
// never 403 and never an allow.
//
// A context prepareRules did not prepare for the bucket is a bug of the caller: authorize logs
// it and answers ErrUnavailable, under authorizeHook too, so the package's tests catch it.
func (s *Service) authorize(ctx context.Context, b Bucket, p Principal, op ruleOp) error {
	if p.Kind == PrincipalAdmin {
		return nil
	}
	rs, ok := preparedRuleset(ctx, b.Project)
	if !ok {
		s.log.Error("storage: authorize without prepareRules", "project_number", b.Project, "method", op.Method.String())
		return fmt.Errorf("%w: the rules were not prepared for project %d", ErrUnavailable, b.Project)
	}
	if s.authorizeHook != nil {
		return s.authorizeHook(ctx, b, p, op)
	}
	start := time.Now()
	allow, err := s.decide(ctx, b, p, op, rs)
	s.metrics.RulesSeconds.Observe(time.Since(start).Seconds())
	switch {
	case errors.Is(err, ErrOverloaded):
		s.metrics.RulesDecisions.WithLabelValues("overloaded").Inc()
		return err
	case err != nil:
		s.log.Error("storage: the rules could not decide", "project_number", b.Project, "method", op.Method.String(), "error", err)
		return err
	case !allow:
		s.metrics.RulesDecisions.WithLabelValues("deny").Inc()
		return ErrPermissionDenied
	}
	s.metrics.RulesDecisions.WithLabelValues("allow").Inc()
	return nil
}

// decide evaluates op under the project's Storage release (ADR-022 §2): a project without one
// has the locked default, and a release that no longer compiles denies everything; tenancy
// answers both.
func (s *Service) decide(ctx context.Context, b Bucket, p Principal, op ruleOp, rs *rules.Ruleset) (bool, error) {
	if rs == nil {
		return false, nil
	}
	req, ok := s.ruleRequest(b, p, op, rs)
	if !ok {
		return false, nil
	}
	d, err := rs.Evaluate(ctx, req)
	switch {
	case errors.Is(err, rules.ErrOverloaded):
		return false, ErrOverloaded
	case err != nil:
		return false, fmt.Errorf("%w: evaluating the Storage rules: %w", ErrUnavailable, err)
	}
	return d.Allow, nil
}

// prepareRules resolves the bucket's Storage ruleset, read by its logical name from the control
// plane (its cache, or the database through the pool), and returns a context that carries it.
// Every operation that decides calls it before any transaction begins, and passes the context
// on to authorize: get and list too, so nothing ever loads inside authorize. Under a row lock, a
// load would need a second connection, and writers that hold every connection of the pool, each
// waiting for another, would deadlock.
//
//	ctx, err := s.prepareRules(ctx, b, p) // Rewrite: once for each of its buckets
//
// An admin needs no ruleset, and a test deciding through authorizeHook reads none, but both get
// the marker authorize checks. The error is ErrUnavailable. The ruleset is the one of the
// request's start: a publish after it applies from the next request.
func (s *Service) prepareRules(ctx context.Context, b Bucket, p Principal) (context.Context, error) {
	if _, ok := preparedRuleset(ctx, b.Project); ok {
		return ctx, nil
	}
	var rs *rules.Ruleset
	if p.Kind != PrincipalAdmin && s.authorizeHook == nil {
		var err error
		rs, err = s.projects.CompiledRelease(ctx, b.Project, tcore.StorageRelease)
		if err != nil {
			return ctx, fmt.Errorf("%w: the project's Storage rules: %w", ErrUnavailable, err)
		}
	}
	prev, _ := ctx.Value(preparedKey{}).([]prepared)
	return context.WithValue(ctx, preparedKey{}, append(slices.Clip(prev), prepared{b.Project, rs})), nil
}

// preparedKey holds the rulesets prepareRules resolved for a request, one per project.
type preparedKey struct{}

type prepared struct {
	project int64
	rs      *rules.Ruleset // nil for an admin and under authorizeHook
}

func preparedRuleset(ctx context.Context, project int64) (*rules.Ruleset, bool) {
	all, _ := ctx.Value(preparedKey{}).([]prepared)
	for _, p := range all {
		if p.project == project {
			return p.rs, true
		}
	}
	return nil, false
}

// ruleRequest builds the engine's request for op, or false where the binding denies without
// evaluating: list under rules_version 1 (core-syntax; the emulator: "List operations are only
// allowed for rules_version='2'", D §4.1), and a path longer than the ruleset's
// request_segments (a name of over a thousand slashes by default), which the engine would refuse
// as an invalid request, answered 503 and retried by the SDKs without end: an Aruma limit
// (docs/reference/compatibility.md).
func (s *Service) ruleRequest(b Bucket, p Principal, op ruleOp, rs *rules.Ruleset) (*rules.Request, bool) {
	list := op.Method == rules.MethodList
	if op.Method < rules.MethodGet || op.Method > rules.MethodDelete || (op.List && !list) {
		return nil, false
	}
	if list && rs.Version() < 2 {
		return nil, false
	}
	match, path := rulePaths(b.Name, op.Name, list)
	if len(match) > rs.RequestSegments() {
		return nil, false
	}
	vars := map[string]rules.Value{
		"request":  s.requestValue(p, op, path),
		"resource": rules.Null(),
	}
	if op.Resource != nil && !list {
		vars["resource"] = existing(*op.Resource)
	}
	return &rules.Request{
		Project:   b.Project,
		Method:    op.Method,
		Path:      match,
		Variables: vars,
		Documents: s.documents,
		Budget:    rules.NewBudget(),
		Inflight:  s.inflight,
	}, true
}

// requestValue is the request variable: auth, path, time, resource, method and params.
//
//   - auth: rules.Auth of the verified ID token, uid its sub (E §7); null for anonymous.
//   - path: b/<bucket>/o/ and the name's segments (aruma/storage/request_path); a list's, its
//     folder's, as the emulator gives it (D §4.2).
//   - time: the time of the evaluation: a finalize's, not the start's.
//   - resource: the incoming object on create and update, null on get, list and delete
//     (provisional, ADR-022 Consequences 3).
//   - method: the method evaluated, and params an empty map, which the firebaserules reference
//     says every service has (provisional, ADR-022 Consequences 2): reading a key of params is
//     an error, a denial.
func (s *Service) requestValue(p Principal, op ruleOp, path []string) rules.Value {
	auth := rules.Null()
	if p.Kind == PrincipalUser {
		auth = rules.Auth(p.UID, p.Token)
	}
	incoming := rules.Null()
	switch op.Method {
	case rules.MethodCreate:
		if op.Incoming != nil {
			o := *op.Incoming
			applyDefaults(&o, op.Surface)
			incoming = incomingCreate(o)
		}
	case rules.MethodUpdate:
		// generation, metageneration and etag are the stored object's whatever Incoming says
		// (PY-RR metageneration-unchanged ALLOW, metageneration-next DENY): a caller's Incoming
		// may already carry the metageneration the change will commit.
		if op.Resource != nil {
			o := patched(*op.Resource, op.Patch)
			if op.Incoming != nil {
				o = *op.Incoming
				o.Generation, o.Metageneration = op.Resource.Generation, op.Resource.Metageneration
			}
			incoming = incomingUpdate(o)
		}
	}
	return rules.Map(map[string]rules.Value{
		"auth":     auth,
		"path":     rules.PathOf(path),
		"time":     rules.Timestamp(s.now()),
		"resource": incoming,
		"method":   rules.String(op.Method.String()),
		"params":   rules.Map(nil),
	})
}

// rulePaths returns the segments the match blocks see and request.path's, from the service
// root: b, the bucket, o, then the object name's segments (segments: the name the surface
// decoded once, split on "/", never decoded again).
//
// A list is decided on the children of its folder (ADR-022 §8, provisional): the folder's
// segments and then rules.UnknownSegment, as the emulator's engine decides it
// (aruma/storage/read_list, aruma/storage-paths/list_on_folder). The folder is the prefix up
// to its last "/": the SDKs list a folder as "<path>/" and the root as "", and a prefix that
// does not end in "/" lists names among its folder's children, which is all the rules may be
// asked about. request.path is the folder's path, without the unknown segment.
func rulePaths(bucket, name string, list bool) (match []rules.Segment, path []string) {
	path = []string{"b", bucket, "o"}
	if !list {
		path = append(path, segments(name)...)
		return rules.Literals(path...), path
	}
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		path = append(path, segments(name[:i])...)
	}
	return append(rules.Literals(path...), rules.UnknownSegment), path
}
