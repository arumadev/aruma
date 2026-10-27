package query

import (
	"strings"

	"github.com/arumadev/aruma/internal/services/documents/index"
	"github.com/arumadev/aruma/internal/services/documents/value"
	"github.com/arumadev/aruma/internal/services/documents/wire"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// Filter is one node of a query's filter tree: a field filter or a unary filter (Path, Op and
// for a field filter its operand), or a composite (Op is index.OpAnd or index.OpOr, with
// Filters). The operators are package index's, so a filter is its own index shape.
type Filter struct {
	Op   index.Operator
	Path value.FieldPath
	// Value is a field filter's operand. For in, not-in and array-contains-any it is an array,
	// whose elements are Values.
	Value  value.Value
	Values []value.Value
	// Filters are a composite's operands.
	Filters []Filter
}

// composite reports whether f is AND or OR.
func (f *Filter) composite() bool { return f.Op == index.OpAnd || f.Op == index.OpOr }

// fieldOps are the field filters' operators, by their wire value.
var fieldOps = map[pb.StructuredQuery_FieldFilter_Operator]index.Operator{
	pb.StructuredQuery_FieldFilter_LESS_THAN:             index.OpLessThan,
	pb.StructuredQuery_FieldFilter_LESS_THAN_OR_EQUAL:    index.OpLessThanOrEqual,
	pb.StructuredQuery_FieldFilter_GREATER_THAN:          index.OpGreaterThan,
	pb.StructuredQuery_FieldFilter_GREATER_THAN_OR_EQUAL: index.OpGreaterThanOrEqual,
	pb.StructuredQuery_FieldFilter_EQUAL:                 index.OpEqual,
	pb.StructuredQuery_FieldFilter_NOT_EQUAL:             index.OpNotEqual,
	pb.StructuredQuery_FieldFilter_ARRAY_CONTAINS:        index.OpArrayContains,
	pb.StructuredQuery_FieldFilter_IN:                    index.OpIn,
	pb.StructuredQuery_FieldFilter_ARRAY_CONTAINS_ANY:    index.OpArrayContainsAny,
	pb.StructuredQuery_FieldFilter_NOT_IN:                index.OpNotIn,
}

var unaryOps = map[pb.StructuredQuery_UnaryFilter_Operator]index.Operator{
	pb.StructuredQuery_UnaryFilter_IS_NAN:      index.OpIsNaN,
	pb.StructuredQuery_UnaryFilter_IS_NULL:     index.OpIsNull,
	pb.StructuredQuery_UnaryFilter_IS_NOT_NAN:  index.OpIsNotNaN,
	pb.StructuredQuery_UnaryFilter_IS_NOT_NULL: index.OpIsNotNull,
}

// opNames are the operators as production's messages name them ('IN' supports up to 30
// comparison values., f0-errors.json in_31).
var opNames = map[index.Operator]string{
	index.OpIn: "IN", index.OpNotIn: "NOT_IN", index.OpArrayContainsAny: "ARRAY_CONTAINS_ANY",
	index.OpNotEqual: "NOT_EQUAL", index.OpArrayContains: "ARRAY_CONTAINS",
}

// filterOf reads a filter off the wire, its field paths and operands checked (validate.go); db
// is the request's database, which a reference operand of __name__ must name. An AND with no
// operand is accepted and filters nothing (where.composite_empty, where.and_nested_empty): it
// is left out of an AND, and at the top it is no filter (empty is set).
func filterOf(f *pb.StructuredQuery_Filter, db wire.Database) (out Filter, empty bool, err error) {
	switch x := f.GetFilterType().(type) {
	case *pb.StructuredQuery_Filter_CompositeFilter:
		var op index.Operator
		switch x.CompositeFilter.GetOp() {
		case pb.StructuredQuery_CompositeFilter_AND:
			op = index.OpAnd
		case pb.StructuredQuery_CompositeFilter_OR:
			op = index.OpOr
		default:
			return Filter{}, false, errUnknownCompositeOp
		}
		subs := x.CompositeFilter.GetFilters()
		if len(subs) == 0 {
			if op == index.OpAnd {
				return Filter{}, true, nil
			}
			return Filter{}, false, errEmptyComposite
		}
		out := Filter{Op: op, Filters: make([]Filter, 0, len(subs))}
		for _, s := range subs {
			c, none, err := filterOf(s, db)
			if err != nil {
				return Filter{}, false, err
			}
			if none && op == index.OpAnd {
				continue
			}
			out.Filters = append(out.Filters, c)
		}
		return out, len(out.Filters) == 0, nil
	case *pb.StructuredQuery_Filter_FieldFilter:
		out, err := fieldFilterOf(x.FieldFilter, db)
		return out, false, err
	case *pb.StructuredQuery_Filter_UnaryFilter:
		op, ok := unaryOps[x.UnaryFilter.GetOp()]
		if !ok {
			return Filter{}, false, errUnknownUnaryOp
		}
		if x.UnaryFilter.GetField() == nil {
			return Filter{}, false, errUnaryNoField
		}
		p, err := filterPathOf(x.UnaryFilter.GetField())
		if err != nil {
			return Filter{}, false, err
		}
		if p.IsDocumentID() {
			// IS_NULL and IS_NAN on __name__ (name.is_null, name.is_nan).
			return Filter{}, false, errNameNotReference
		}
		return Filter{Op: op, Path: p}, false, nil
	}
	return Filter{}, false, errEmptyFilter
}

func fieldFilterOf(ff *pb.StructuredQuery_FieldFilter, db wire.Database) (Filter, error) {
	op, ok := fieldOps[ff.GetOp()]
	if !ok {
		return Filter{}, errUnknownFieldOp
	}
	p, err := filterPathOf(ff.GetField())
	if err != nil {
		return Filter{}, err
	}
	if ff.GetValue() == nil {
		return Filter{}, wire.InvalidArgument(msgNoValue)
	}
	v, err := wire.ValueFromProto(ff.GetValue())
	if err != nil {
		return Filter{}, err
	}
	out := Filter{Op: op, Path: p, Value: v}
	switch op {
	case index.OpIn, index.OpNotIn, index.OpArrayContainsAny:
		if v.Kind() != value.KindArray {
			return Filter{}, errNeedsArray(op, false)
		}
		out.Values = v.Elements()
		if len(out.Values) == 0 {
			return Filter{}, errNeedsArray(op, true)
		}
		limit := maxDisjuncts
		if op == index.OpNotIn {
			limit = maxNotIn
		}
		if len(out.Values) > limit {
			return Filter{}, errTooManyValues(op, limit)
		}
		for _, e := range out.Values {
			if err := checkOperand(p, e); err != nil {
				return Filter{}, err
			}
		}
	default:
		if err := checkOperand(p, v); err != nil {
			return Filter{}, err
		}
	}
	if p.IsDocumentID() {
		if err := checkNameOperand(op, out, db); err != nil {
			return Filter{}, err
		}
	}
	return out, nil
}

// checkNameOperand checks a filter on __name__: it is a comparison, an equality or a
// membership, never a containment ("the name __key__ is reserved", name.array_contains), and
// its operands are references (name.eq_string, name.in_mixed) to documents of the request's
// database (name.eq_other_project).
func checkNameOperand(op index.Operator, f Filter, db wire.Database) error {
	operands := []value.Value{f.Value}
	switch op {
	case index.OpArrayContains, index.OpArrayContainsAny:
		return errNameReserved
	case index.OpIn, index.OpNotIn:
		operands = f.Values
	}
	for _, e := range operands {
		if e.Kind() != value.KindReference {
			return errNameNotReference
		}
	}
	for _, e := range operands {
		if n, err := wire.ParseReference(e.StringValue()); err == nil && n.Database != db {
			return errOtherDatabase(db, n.Database)
		}
	}
	return nil
}

// fieldPathOf parses a field reference's path, with production's messages for a path
// (values-mask.json; value.ParseFieldPath).
func fieldPathOf(r *pb.StructuredQuery_FieldReference) (value.FieldPath, error) {
	p, err := value.ParseFieldPath(r.GetFieldPath())
	if err != nil {
		return nil, wire.InvalidArgument(err.Error())
	}
	return p, nil
}

// filterPathOf parses the path of a filter or an order: a field path, which may not hold a
// reserved name ("Invalid reserved name in field path __x__", path.reserved, reserved_nested),
// nor __name__ but as the whole path ("the name __key__ is reserved", path.name_nested,
// name_under_map).
func filterPathOf(r *pb.StructuredQuery_FieldReference) (value.FieldPath, error) {
	p, err := fieldPathOf(r)
	if err != nil || p.IsDocumentID() {
		return p, err
	}
	for _, s := range p {
		switch {
		case s == value.DocumentID:
			return nil, errNameReserved
		case reservedName(s):
			return nil, errReservedName(s)
		}
	}
	return p, nil
}

// reservedName reports whether a field name is reserved: two underscores, at least one
// character, two underscores (value.Validate's rule).
func reservedName(s string) bool {
	return len(s) >= 5 && strings.HasPrefix(s, "__") && strings.HasSuffix(s, "__")
}

// msgNoValue is a Value with no kind set (f1-wire-errors.json value.no_type_in_filter).
const msgNoValue = "Cannot convert firestore.v1.Value with type unset."

// shapeFilter is f as package index reads it.
func shapeFilter(f *Filter) index.Filter {
	if f.composite() {
		out := index.Filter{Op: f.Op, Filters: make([]index.Filter, len(f.Filters))}
		for i := range f.Filters {
			out.Filters[i] = shapeFilter(&f.Filters[i])
		}
		return out
	}
	return index.Where(f.Path.String(), f.Op)
}
