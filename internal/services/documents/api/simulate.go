package api

import (
	"context"
	"errors"

	"github.com/arumadev/aruma/internal/platform/rules"
	"github.com/arumadev/aruma/internal/services/documents/core"
	"github.com/arumadev/aruma/internal/services/documents/wire"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// Simulation is one request the dashboard's Rules Playground asks the rules about, as the
// Firebase console's does (f7-production-console.md §2): a get, create, update or delete of a
// document, by an end user or by no one, under a ruleset that need not be published.
type Simulation struct {
	// Document is the document's full name, projects/{p}/databases/{d}/documents/….
	Document string
	// Method is rules.MethodGet, MethodCreate, MethodUpdate or MethodDelete.
	Method rules.Method
	// Ruleset is the rules to judge by; nil is the project's published release (none, the
	// locked default).
	Ruleset *rules.Ruleset
	// Authenticated is whether request.auth is AuthUID and AuthToken's claims, or null.
	Authenticated bool
	AuthUID       string
	AuthToken     map[string]any
	// Fields are request.resource's data, of a create or an update: the document as the write
	// would leave it.
	Fields map[string]*pb.Value
}

// SimulationResult is the rules' decision on a Simulation, and the allow statements they
// evaluated, in order, with their outcomes.
type SimulationResult struct {
	Allow      bool
	Statements []rules.StatementResult
}

// SimulateRules judges s as judge judges a real request of an end user: the document as stored
// read as resource, request.resource the given fields, get() and exists() reading the
// project's documents at the latest commit (getAfter() of a write seeing its document as
// written), under the same lookup budgets and evaluation limits. It writes nothing, and its
// decision is not counted in the usage. Only an admin of the project may ask: the caller must
// already be allowed to read every document get() could. A decision the rules cannot make is
// UNAVAILABLE, as for a real request.
func (s *Service) SimulateRules(ctx context.Context, c Call, sim Simulation) (SimulationResult, error) {
	a, err := s.admitAs(ctx, c, &pb.GetDocumentRequest{Name: sim.Document}, documentOf(sim.Document), true)
	if err != nil {
		return SimulationResult{}, err
	}
	n, err := wire.ParseDocumentName(sim.Document)
	if err != nil {
		return SimulationResult{}, err
	}
	path := pathOf(n)
	op := ruleOp{method: sim.Method, path: path, before: core.Document{Path: path}, now: s.now()}
	switch sim.Method {
	case rules.MethodGet, rules.MethodDelete:
	case rules.MethodCreate, rules.MethodUpdate:
		fields, err := fieldsOf(&pb.Document{Fields: sim.Fields})
		if err != nil {
			return SimulationResult{}, err
		}
		op.after = core.Document{Path: path, Fields: fields, CreateTime: op.now, UpdateTime: op.now}
	default:
		return SimulationResult{}, wire.InvalidArgument("The simulated method must be get, create, update or delete.")
	}

	rs := sim.Ruleset
	if rs == nil {
		if rs, err = s.ruleset(ctx, a); err != nil {
			return SimulationResult{}, err
		}
	}
	before, _, err := s.o.Store.Get(ctx, a.db, path, core.ReadOptions{})
	switch ce, _ := errors.AsType[*core.Error](err); {
	case ce != nil && ce.Code == core.NotFound:
	case err != nil:
		return SimulationResult{}, s.fail(ctx, err)
	default:
		op.before = before
		if sim.Method == rules.MethodUpdate || sim.Method == rules.MethodCreate {
			op.after.CreateTime = before.CreateTime
		}
	}
	// The lookups and budgets of a real read, or of a commit of this one write (judged).
	l := &lookups{store: s.o.Store, db: a.db}
	op.lookups, op.budget = l, readBudget()
	if sim.Method != rules.MethodGet {
		l.after = map[string]core.Document{path.String(): op.after}
		op.budget = rules.NewBudget()
		op.budget.Exempt(documentPath(a.db, path))
	}
	// The simulated caller: an end user whom the rules hold, whoever asks.
	held := a
	held.guard = &guard{auth: rules.Null()}
	if sim.Authenticated {
		held.guard.auth = rules.Auth(sim.AuthUID, sim.AuthToken)
	}
	d, err := s.decide(ctx, held, rs, op, rules.TraceStatements)
	if we, ok := errors.AsType[*wire.Error](err); err != nil && (!ok || we.Code != wire.CodePermissionDenied) {
		return SimulationResult{}, err
	}
	return SimulationResult{Allow: err == nil && d.Allow, Statements: d.Statements}, nil
}
