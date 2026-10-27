package query

import (
	"slices"
	"strconv"
	"strings"

	"github.com/arumadev/aruma/internal/services/documents/index"
	"github.com/arumadev/aruma/internal/services/documents/value"
	"github.com/arumadev/aruma/internal/services/documents/wire"
)

// Production's limits on a query (ADR-025 §1; OF §2b; f0-errors.json; f3-query-limits.json).
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
	// maxQueryElements bounds a disjunct's filters, the explicit orders and the ancestor (one
	// when the parent is a document) together: 100 filters at the root pass, 101 fail, and 100
	// under a document fail (filters.eq_100, eq_101, eq_100_subcollection, eq_99_subcollection).
	maxQueryElements = 100
)

// The errors of a query's checks, with production's texts (f3-query-limits.json,
// f3-run-query.json, f0-descendants.json, f1-wire-errors.json).
var (
	errGroupNotAtRoot     = wire.InvalidArgument("collection group queries are only allowed at the root parent")
	errAfterKey           = wire.InvalidArgument("order by clause cannot contain more fields after the key")
	errUnknownDirection   = wire.InvalidArgument("Unrecognized ordering.")
	errUnknownFieldOp     = wire.InvalidArgument("Unknown FieldFilter operator.")
	errUnknownUnaryOp     = wire.InvalidArgument("Unknown UnaryFilter operator.")
	errUnaryNoField       = wire.InvalidArgument("Unsupported UnaryFilter operand type (non-FIELD).")
	errUnknownCompositeOp = wire.InvalidArgument("Unsupported CompositeFilter operator.")
	errEmptyComposite     = wire.InvalidArgument("Composite filter must have at least one sub-filter.")
	errEmptyFilter        = wire.InvalidArgument("Unknown Filter type.")
	errNameNotReference   = wire.InvalidArgument("__key__ filter value must be a Key")
	errNameReserved       = wire.InvalidArgument("the name __key__ is reserved")
	errTwoNotEqual        = wire.InvalidArgument("Only a single 'NOT_EQUAL', 'NOT_IN', 'IS_NOT_NAN', or 'IS_NOT_NULL' filter allowed per query.")
	errNotInWithDisjuncts = wire.InvalidArgument("'NOT_IN' cannot be used in the same query with 'IN', 'ARRAY_CONTAINS_ANY' or 'OR'.")
	errTwoContains        = wire.InvalidArgument("A maximum of 1 'ARRAY_CONTAINS' filter is allowed per disjunction.")
	errTwoFrom            = wire.InvalidArgument("StructuredQuery.from cannot have more than one collection selector.")
	errNoQuery            = wire.InvalidArgument("only structured queries are supported")
	errNegativeLimit      = wire.InvalidArgument("limit is negative")
	errNegativeOffset     = wire.InvalidArgument("offset is negative")
	errCursorValues       = wire.InvalidArgument("Cursor has too many values.")
	errCursorName         = wire.InvalidArgument("Cursor __key__ value is not a document reference.")
	errKindlessOrder      = wire.InvalidArgument("kind is required for all orders except __key__ ascending")
)

// errKindlessFilter is a kindless query's filter on a field (from.none).
func errKindlessFilter(p value.FieldPath) error {
	return wire.InvalidArgument("kind is required for filter: " + p.String())
}

// errOrderDuplicate is an explicit order on one field twice (order.v_twice, order.v_asc_v_desc).
func errOrderDuplicate(p value.FieldPath) error {
	return wire.InvalidArgument("order by clause cannot contain duplicate fields " + p.String())
}

// errReservedName is a reserved name in a field path (path.reserved, path.reserved_nested).
func errReservedName(name string) error {
	return wire.InvalidArgument("Invalid reserved name in field path " + name)
}

// errInequalityFields names the inequality fields of a query with more than ten. Production
// lists them in an order of its own set (ineq.11_fields: [f8, f9, f6, f7, f4, f5, f2, f3, f0,
// f1, f10] for f0 … f10), which no rule known here reproduces; they are listed in the request's
// order (parity exception inequality-fields-order).
func errInequalityFields(paths []value.FieldPath) error {
	names := make([]string, len(paths))
	for i, p := range paths {
		names[i] = p.String()
	}
	return wire.InvalidArgument("The query contains " + strconv.Itoa(len(paths)) + " distinct inequality fields: [" +
		strings.Join(names, ", ") + "]. A query may not have more than " + strconv.Itoa(maxInequalityFields) +
		" distinct inequality fields.")
}

// errQueryElements is a query over maxQueryElements, with its counts (filters.eq_101,
// eq_100_subcollection, eq_60_orders_41, orders_101).
func errQueryElements(filters, orders int, ancestor bool) error {
	a := "no ancestor filter"
	if ancestor {
		a = "one ancestor filter"
	}
	return wire.InvalidArgument("The query may not have more than 100 filters + sort orders + ancestor total. Currently there are " +
		strconv.Itoa(filters) + " filters, " + strconv.Itoa(orders) + " sort orders, and " + a + ".")
}

// errOtherDatabase is a reference operand of __name__ in another database than the request's
// (name.eq_other_project): wire's binding text.
func errOtherDatabase(req, other wire.Database) error {
	return wire.InvalidArgument("The request was for database '" + req.Name() +
		"' but was attempting to access database '" + other.Name() + "'")
}

// errNeedsArray is an in, not-in or array-contains-any whose operand is not an array, or is an
// empty one, in production's grammar (in.not_array, in.empty).
func errNeedsArray(op index.Operator, empty bool) error {
	if empty {
		return wire.InvalidArgument("'" + opNames[op] + "' requires an non-empty ArrayValue.")
	}
	return wire.InvalidArgument("'" + opNames[op] + "' requires an ArrayValue.")
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

// checkFilter checks the rules of the whole filter tree: the DNF bound, one not-in, !=,
// IS_NOT_NULL or IS_NOT_NAN per query, not-in with no disjunction, at most ten inequality
// fields, at most one array-contains per disjunct, and the filters of each disjunct with the
// explicit orders and the ancestor.
func checkFilter(f *Filter, orders int, ancestor bool) error {
	if f == nil {
		return checkElements(0, orders, ancestor)
	}
	n := disjunctCount(f)
	if n > maxDisjuncts {
		return errTooManyDisjuncts(n)
	}
	var notEqual, disjunctive int
	walk(f, func(l *Filter) {
		switch l.Op {
		case index.OpNotIn, index.OpNotEqual, index.OpIsNotNull, index.OpIsNotNaN:
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
	if paths := requestInequalityPaths(f); len(paths) > maxInequalityFields {
		return errInequalityFields(paths)
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
		if err := checkElements(len(conj), orders, ancestor); err != nil {
			return err
		}
	}
	return nil
}

// requestInequalityPaths are the inequality fields of the tree but __name__, each once, in the
// order the request first names them.
func requestInequalityPaths(f *Filter) []value.FieldPath {
	var out []value.FieldPath
	walk(f, func(l *Filter) {
		if inequality(l.Op) && !l.Path.IsDocumentID() && !slices.ContainsFunc(out, func(p value.FieldPath) bool { return slices.Equal(p, l.Path) }) {
			out = append(out, l.Path)
		}
	})
	return out
}

func checkElements(filters, orders int, ancestor bool) error {
	n := filters + orders
	if ancestor {
		n++
	}
	if n > maxQueryElements {
		return errQueryElements(filters, orders, ancestor)
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
