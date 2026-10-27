package query

import (
	"strconv"

	"github.com/arumadev/aruma/internal/services/documents/index"
	"github.com/arumadev/aruma/internal/services/documents/value"
	"github.com/arumadev/aruma/internal/services/documents/wire"
)

// Production's limits on a query (ADR-025 §1; OF §2b; f0-errors.json).
const (
	// maxDisjuncts bounds the filter's disjunctive normal form, and the values of in and
	// array-contains-any: 30 pass, 31 fail (or_30_same_field, or_31_*, in_30, in_31,
	// and_in_6x6).
	maxDisjuncts = 30
	// maxNotIn bounds not-in's values: 10 pass, 11 fail (not_in_10, not_in_11).
	maxNotIn = 10
	// maxInequalityFields bounds the fields under a range or inequality filter (OF §2b,
	// "Cloud Firestore limits the number of range or inequality fields to 10").
	maxInequalityFields = 10
	// maxQueryElements bounds a disjunct's filters, the orders and the parent's depth together
	// (OF §2b).
	maxQueryElements = 100
)

// The errors of a query's checks. Each carries production's text where it was recorded; the
// others are provisional (wire/errors.go), the closest form known, until the recorder captures
// production's (ADR-028 §7.6).
var (
	// f0-descendants.json rest.kindless_with_field_filter, rest.kindless_order_by_field.
	errGroupNotAtRoot = wire.InvalidArgument("collection group queries are only allowed at the root parent")
	// f0-descendants.json rest.partition.with_filter: an order after __name__, which an
	// inequality on another field appends when __name__ is ordered explicitly.
	errAfterKey = wire.InvalidArgument("order by clause cannot contain more fields after the key")
	// f1-wire-errors.json json.bad_enum_number: a direction outside the enum.
	errUnknownDirection = wire.InvalidArgument("Unrecognized ordering.")

	// Provisional texts.
	errUnknownOperator    = wire.InvalidArgument("Unrecognized operator.")
	errUnknownCompositeOp = wire.InvalidArgument("Unrecognized composite filter operator.")
	errEmptyComposite     = wire.InvalidArgument("A composite filter must contain at least one filter.")
	errEmptyFilter        = wire.InvalidArgument("A filter must set one of composite_filter, field_filter or unary_filter.")
	errNameNotReference   = wire.InvalidArgument("__key__ filter value must be a Key")
	errNameArrayFilter    = wire.InvalidArgument("__key__ cannot be used in an array filter")
	errTwoNotEqual        = wire.InvalidArgument("A query can have at most one 'NOT_IN' or 'NOT_EQUAL' filter.")
	errNotInWithDisjuncts = wire.InvalidArgument("'NOT_IN' cannot be used in the same query with 'IN', 'ARRAY_CONTAINS_ANY' or 'OR'.")
	errTwoContains        = wire.InvalidArgument("A disjunction can have at most one 'ARRAY_CONTAINS' or 'ARRAY_CONTAINS_ANY' filter.")
	errInequalityFields   = wire.InvalidArgument("A query can have range and inequality filters on at most 10 fields.")
	errQueryElements      = wire.InvalidArgument("A query's filters, orders and parent depth together cannot exceed 100.")
	errNoFrom             = wire.InvalidArgument("A query must select exactly one collection.")
	errNoQuery            = wire.InvalidArgument("The request has no structured_query.")
	errNegativeLimit      = wire.InvalidArgument("limit must be non-negative.")
	errNegativeOffset     = wire.InvalidArgument("offset must be non-negative.")
	errCursorValues       = wire.InvalidArgument("A cursor cannot have more values than the query has orders.")
	errCursorName         = wire.InvalidArgument("__key__ cursor value must be a Key")
	errKindlessOrder      = wire.InvalidArgument("kind is required for all orders except __key__ ascending")
)

// errNeedsArray is an in, not-in or array-contains-any whose operand is not a non-empty array
// (provisional).
func errNeedsArray(op index.Operator) error {
	return wire.InvalidArgument("'" + opNames[op] + "' requires a non-empty ArrayValue.")
}

// errTooManyValues is f0-errors.json in_31 and not_in_11's text; array-contains-any's is
// inferred from them.
func errTooManyValues(op index.Operator, limit int) error {
	return wire.InvalidArgument("'" + opNames[op] + "' supports up to " + strconv.Itoa(limit) + " comparison values.")
}

// errTooManyDisjuncts is f0-errors.json or_31_* and and_in_6x6's text.
func errTooManyDisjuncts(n int) error {
	return wire.InvalidArgument("Too many disjunctions after normalization. Result had " + strconv.Itoa(n) +
		" disjunctions which is more than the maximum of 30")
}

// checkOperand checks a filter's operand on field p as production does before it runs the
// query: a value the index cannot hold whole is "too large to be used in a query"
// (values-equality.json, the == and in of the long values), and a vector or BSON shape must be
// valid (value.ValidateOperand).
func checkOperand(p value.FieldPath, v value.Value) error {
	if !value.IndexedWhole(v) {
		return wire.InvalidArgument("value for " + p.String() + " is too large to be used in a query")
	}
	if err := value.ValidateOperand(v); err != nil {
		return wire.InvalidArgument(err.Error())
	}
	return nil
}

// checkFilter checks the rules of the whole filter tree: the DNF bound, one not-in or != per
// query, not-in with no disjunction, at most one array-contains per disjunct, at most ten
// inequality fields, and the elements of each disjunct with the orders and the parent's depth.
func checkFilter(f *Filter, orders, depth int) error {
	if f == nil {
		return checkElements(0, orders, depth)
	}
	n := disjunctCount(f)
	if n > maxDisjuncts {
		return errTooManyDisjuncts(n)
	}
	var notEqual, disjunctive int
	walk(f, func(l *Filter) {
		switch l.Op {
		case index.OpNotIn, index.OpNotEqual:
			notEqual++
		case index.OpIn, index.OpArrayContainsAny, index.OpOr:
			disjunctive++
		}
	})
	if notEqual > 1 {
		return errTwoNotEqual
	}
	if notEqual > 0 && hasOp(f, index.OpNotIn) && disjunctive > 0 {
		return errNotInWithDisjuncts
	}
	if len(inequalityPaths(f)) > maxInequalityFields {
		return errInequalityFields
	}
	for _, conj := range disjuncts(f) {
		contains := 0
		for _, l := range conj {
			if l.Op == index.OpArrayContains || l.Op == index.OpArrayContainsAny {
				contains++
			}
		}
		if contains > 1 {
			return errTwoContains
		}
		if err := checkElements(len(conj), orders, depth); err != nil {
			return err
		}
	}
	return nil
}

func checkElements(filters, orders, depth int) error {
	if filters+orders+depth > maxQueryElements {
		return errQueryElements
	}
	return nil
}

// walk calls fn on every node of the tree, composites included.
func walk(f *Filter, fn func(*Filter)) {
	fn(f)
	for i := range f.Filters {
		walk(&f.Filters[i], fn)
	}
}

func hasOp(f *Filter, op index.Operator) bool {
	found := false
	walk(f, func(l *Filter) { found = found || l.Op == op })
	return found
}
