package api

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/arumadev/aruma/internal/platform/rules"
	"github.com/arumadev/aruma/internal/services/documents/core"
	"github.com/arumadev/aruma/internal/services/documents/index"
	"github.com/arumadev/aruma/internal/services/documents/query"
	"github.com/arumadev/aruma/internal/services/documents/value"
	"github.com/arumadev/aruma/internal/services/documents/wire"
)

// The rules' list (ADR-021 §2.4): a query is decided before any document is read, by what its
// filters say of the documents it can return (f5-rules-queries.json, f5-rules-constraints.json).
//
// The query is split into ways: each disjunct of its filter, and in each, each value of an in
// (an ==) and of an array-contains-any (an array-contains). Each way is evaluated on allow list
// at the collection's path with an unknown document id ({path=**} takes the unknown run of a
// collection group, a plain match does not: cg.group-plain-match), with resource.data a
// rules.PartialMap of the fields the way filters on, and the query is allowed when every way
// is. resource.id and resource.__name__ are unknown, as is request.path of a collection group
// or of every collection under a parent; a collection's is its path (§4). request.query is
// rules.Query: the limit, or null; the offset, or 0; the orders given. A filter on __name__ and
// IS_NOT_NAN say nothing the rules read (unrecorded; leaving a filter out never allows more).

// listQuery is what the rules see of a list: where it reads and the query.
type listQuery struct {
	parent     core.Path // the parent document, empty for the root
	collection string    // "" for every collection under the parent
	group      bool      // the collections of that id anywhere below the parent
	where      *query.Filter
	limit      *int64 // nil for none
	offset     int64
	orders     int // the orders given explicitly
}

// listOf is a parsed query's list under parent.
func listOf(parent wire.Name, q *query.Query) listQuery { return listOfPath(pathOf(parent), q) }

// listOfPath is a parsed query's list under the document parent (empty: the root).
func listOfPath(parent core.Path, q *query.Query) listQuery {
	lq := listQuery{parent: parent, collection: q.CollectionID, group: q.AllDescendants,
		where: q.Where, offset: int64(q.Offset), orders: len(q.Explicit)}
	if q.Limit >= 0 {
		lq.limit = new(int64(q.Limit))
	}
	return lq
}

// maxWays bounds the ways a query is split into. query.Parse has bounded its disjuncts to 30
// already; a query past this is denied rather than evaluated.
const maxWays = 1 << 10

// allowList decides a list read at readTime (zero: the latest) for a's caller: nil when the rules
// allow every way of it, wire.PermissionDenied when they do not.
func (s *Service) allowList(ctx context.Context, a admitted, lq listQuery, readTime time.Time) error {
	if a.guard == nil {
		return nil
	}
	rs, err := s.ruleset(ctx, a)
	if err != nil {
		return err
	}
	return s.judgeList(ctx, a, rs, lq, &lookups{store: s.o.Store, db: a.db, readTime: readTime, usage: s.o.Usage}, rules.NewPooledBudget(), s.now())
}

// judgeList decides lq under rs, every way of it sharing the lookups and their budget. The usage
// counts the decision, once.
func (s *Service) judgeList(ctx context.Context, a admitted, rs *rules.Ruleset, lq listQuery, l rules.Documents,
	budget *rules.Budget, now time.Time) error {
	return s.countRule(ctx, a, s.decideList(ctx, a, rs, lq, l, budget, now))
}

func (s *Service) decideList(ctx context.Context, a admitted, rs *rules.Ruleset, lq listQuery, l rules.Documents,
	budget *rules.Budget, now time.Time) error {
	if rs == nil {
		return wire.PermissionDenied()
	}
	ways, ok := listWays(lq.where)
	if !ok {
		return wire.PermissionDenied()
	}
	match := rules.Literals(documentPath(a.db, lq.parent)...)
	if lq.group {
		match = append(match, rules.UnknownRun)
	}
	path := rules.Unknown()
	if lq.collection == "" {
		match = append(match, rules.UnknownSegment)
	} else {
		match = append(match, rules.Literal(lq.collection))
		if !lq.group {
			path = rules.PathOf(documentPath(a.db, append(lq.parent[:len(lq.parent):len(lq.parent)], lq.collection)))
		}
	}
	match = append(match, rules.UnknownSegment)
	if len(match) > rs.RequestSegments() {
		return wire.PermissionDenied()
	}
	request := rules.Map(map[string]rules.Value{
		"auth":     a.guard.auth,
		"method":   rules.String(rules.MethodList.String()),
		"path":     path,
		"time":     rules.Timestamp(now),
		"resource": rules.Null(),
		"query":    rules.Query(lq.limit, lq.offset, lq.orders),
	})
	for _, w := range ways {
		resource := rules.Map(map[string]rules.Value{
			"data": wayData(w), "id": rules.Unknown(), "__name__": rules.Unknown(),
		})
		d, err := rs.Evaluate(ctx, &rules.Request{
			Project:   a.db.Project,
			Method:    rules.MethodList,
			Path:      match,
			Variables: map[string]rules.Value{"request": request, "resource": resource},
			Documents: l,
			Budget:    budget,
			Inflight:  s.inflight,
		})
		switch {
		case errors.Is(err, rules.ErrOverloaded):
			return errRulesUnavailable
		case err != nil:
			s.log.Error("documents: the rules could not decide", "project_number", a.db.Project, "method", "list", "error", err)
			return errRulesUnavailable
		case !d.Allow:
			return wire.PermissionDenied()
		}
	}
	return nil
}

// wayFilter is one filter of a way, on a field.
type wayFilter struct {
	path value.FieldPath
	f    rules.Filter
}

// listWays splits where into its ways; false when there are more than maxWays. No filter is one
// way that knows no field.
func listWays(where *query.Filter) ([][]wayFilter, bool) {
	if where == nil {
		return [][]wayFilter{nil}, true
	}
	var out [][]wayFilter
	for _, conj := range query.Disjuncts(where) {
		ways := [][]wayFilter{nil}
		for _, f := range conj {
			alts := wayFilters(f)
			if len(alts) == 0 {
				continue // says nothing the rules read
			}
			if len(ways)*len(alts) > maxWays {
				return nil, false
			}
			next := make([][]wayFilter, 0, len(ways)*len(alts))
			for _, w := range ways {
				for _, alt := range alts {
					next = append(next, append(w[:len(w):len(w)], alt))
				}
			}
			ways = next
		}
		if len(out)+len(ways) > maxWays {
			return nil, false
		}
		out = append(out, ways...)
	}
	return out, true
}

// wayFilters are the alternatives a field filter makes: one filter, or one per value of an in or
// an array-contains-any; none for a filter the rules read nothing of.
func wayFilters(f *query.Filter) []wayFilter {
	if f.Path.IsDocumentID() {
		return nil
	}
	one := func(op rules.Comparator, v rules.Value) []wayFilter {
		return []wayFilter{{path: f.Path, f: rules.Filter{Op: op, Value: v}}}
	}
	each := func(op rules.Comparator) []wayFilter {
		out := make([]wayFilter, len(f.Values))
		for i, v := range f.Values {
			out[i] = wayFilter{path: f.Path, f: rules.Filter{Op: op, Value: ruleValue(v)}}
		}
		return out
	}
	switch f.Op {
	case index.OpEqual:
		return one(rules.FilterEQ, ruleValue(f.Value))
	case index.OpNotEqual:
		return one(rules.FilterNE, ruleValue(f.Value))
	case index.OpLessThan:
		return one(rules.FilterLT, ruleValue(f.Value))
	case index.OpLessThanOrEqual:
		return one(rules.FilterLE, ruleValue(f.Value))
	case index.OpGreaterThan:
		return one(rules.FilterGT, ruleValue(f.Value))
	case index.OpGreaterThanOrEqual:
		return one(rules.FilterGE, ruleValue(f.Value))
	case index.OpArrayContains:
		return one(rules.FilterContains, ruleValue(f.Value))
	case index.OpIn:
		return each(rules.FilterEQ)
	case index.OpArrayContainsAny:
		return each(rules.FilterContains)
	case index.OpNotIn:
		vs := make([]rules.Value, len(f.Values))
		for i, v := range f.Values {
			vs[i] = ruleValue(v)
		}
		return one(rules.FilterNotIn, rules.List(vs))
	case index.OpIsNull:
		return one(rules.FilterEQ, rules.Null())
	case index.OpIsNotNull:
		return one(rules.FilterNE, rules.Null())
	case index.OpIsNaN:
		return one(rules.FilterEQ, rules.Float(math.NaN()))
	}
	return nil
}

// wayData is resource.data of a way: each field it filters on a Constraint of its filters, a
// field path a.b a PartialMap holding b. A field filtered on both itself and below keeps its own
// filters alone.
func wayData(w []wayFilter) rules.Value {
	type node struct {
		filters []rules.Filter
		fields  map[string]*node
	}
	root := &node{}
	for _, f := range w {
		n := root
		for _, seg := range f.path {
			if n.fields == nil {
				n.fields = map[string]*node{}
			}
			next, ok := n.fields[seg]
			if !ok {
				next = &node{}
				n.fields[seg] = next
			}
			n = next
		}
		n.filters = append(n.filters, f.f)
	}
	var build func(n *node) rules.Value
	build = func(n *node) rules.Value {
		if len(n.filters) > 0 {
			return rules.Constraint(n.filters...)
		}
		fields := make(map[string]rules.Value, len(n.fields))
		for k, c := range n.fields {
			fields[k] = build(c)
		}
		return rules.PartialMap(fields)
	}
	return build(root)
}
