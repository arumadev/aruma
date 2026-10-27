package query

import (
	"slices"

	"github.com/arumadev/aruma/internal/services/documents/index"
	"github.com/arumadev/aruma/internal/services/documents/value"
)

// disjunctCount is the number of disjuncts of f's disjunctive normal form as production counts
// them (OF §2b, "limited to 30 disjunctions based on the query's disjunctive normal form"): an
// in or array-contains-any of n values is n disjuncts, an OR sums its operands, an AND
// multiplies them. An OR of 31 equalities on one field is 31, not one in (f0-errors.json
// or_31_same_field); in [1..6] && in [1..6] is 36 (and_in_6x6). The count saturates past any
// bound that matters, so a deep tree cannot overflow it.
func disjunctCount(f *Filter) int {
	const ceiling = 1 << 20
	switch f.Op {
	case index.OpIn, index.OpArrayContainsAny:
		return max(len(f.Values), 1)
	case index.OpOr:
		n := 0
		for i := range f.Filters {
			n = min(n+disjunctCount(&f.Filters[i]), ceiling)
		}
		return n
	case index.OpAnd:
		n := 1
		for i := range f.Filters {
			n = min(n*disjunctCount(&f.Filters[i]), ceiling)
		}
		return n
	}
	return 1
}

// disjuncts expands the AND and OR of f into the conjunctions of its leaves, an in or
// array-contains-any left whole. The query's checks have bounded the expansion
// (disjunctCount): it is called after them.
func disjuncts(f *Filter) [][]*Filter {
	switch f.Op {
	case index.OpOr:
		var out [][]*Filter
		for i := range f.Filters {
			out = append(out, disjuncts(&f.Filters[i])...)
		}
		return out
	case index.OpAnd:
		out := [][]*Filter{nil}
		for i := range f.Filters {
			sub := disjuncts(&f.Filters[i])
			next := make([][]*Filter, 0, len(out)*len(sub))
			for _, a := range out {
				for _, b := range sub {
					next = append(next, slices.Concat(a, b))
				}
			}
			out = next
		}
		return out
	}
	return [][]*Filter{{f}}
}

// Disjuncts are the conjunctions of f's disjunctive normal form, an in or array-contains-any
// left whole: the ways the security rules judge a list by (api/rules_list.go). f is a parsed
// query's, whose disjuncts Parse has bounded.
func Disjuncts(f *Filter) [][]*Filter { return disjuncts(f) }

// inequalityPaths are the fields under a range or inequality filter anywhere in the tree, but
// __name__, in field path order, each once: the fields an order must hold (query.proto,
// StructuredQuery.order_by).
func inequalityPaths(f *Filter) []value.FieldPath {
	if f == nil {
		return nil
	}
	var out []value.FieldPath
	walk(f, func(l *Filter) {
		if inequality(l.Op) && !l.Path.IsDocumentID() {
			out = append(out, l.Path)
		}
	})
	slices.SortFunc(out, comparePaths)
	return slices.CompactFunc(out, func(a, b value.FieldPath) bool { return comparePaths(a, b) == 0 })
}

// inequality reports the operators that imply an order on their field: <, <=, >, >=, !=,
// not-in, IS_NOT_NULL and IS_NOT_NAN.
func inequality(op index.Operator) bool {
	switch op {
	case index.OpLessThan, index.OpLessThanOrEqual, index.OpGreaterThan, index.OpGreaterThanOrEqual,
		index.OpNotEqual, index.OpNotIn, index.OpIsNotNull, index.OpIsNotNaN:
		return true
	}
	return false
}

// comparePaths orders field paths segment by segment, as Firestore's FieldPath does (and
// package index, which suggests indexes in that order).
func comparePaths(a, b value.FieldPath) int { return slices.Compare(a, b) }
