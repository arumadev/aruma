package query

import (
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

// filterOf reads a filter off the wire, its field paths and operands checked (validate.go).
func filterOf(f *pb.StructuredQuery_Filter) (Filter, error) {
	switch x := f.GetFilterType().(type) {
	case *pb.StructuredQuery_Filter_CompositeFilter:
		var op index.Operator
		switch x.CompositeFilter.GetOp() {
		case pb.StructuredQuery_CompositeFilter_AND:
			op = index.OpAnd
		case pb.StructuredQuery_CompositeFilter_OR:
			op = index.OpOr
		default:
			return Filter{}, errUnknownCompositeOp
		}
		subs := x.CompositeFilter.GetFilters()
		if len(subs) == 0 {
			return Filter{}, errEmptyComposite
		}
		out := Filter{Op: op, Filters: make([]Filter, 0, len(subs))}
		for _, s := range subs {
			c, err := filterOf(s)
			if err != nil {
				return Filter{}, err
			}
			out.Filters = append(out.Filters, c)
		}
		return out, nil
	case *pb.StructuredQuery_Filter_FieldFilter:
		return fieldFilterOf(x.FieldFilter)
	case *pb.StructuredQuery_Filter_UnaryFilter:
		op, ok := unaryOps[x.UnaryFilter.GetOp()]
		if !ok {
			return Filter{}, errUnknownOperator
		}
		p, err := fieldPathOf(x.UnaryFilter.GetField())
		if err != nil {
			return Filter{}, err
		}
		return Filter{Op: op, Path: p}, nil
	}
	return Filter{}, errEmptyFilter
}

func fieldFilterOf(ff *pb.StructuredQuery_FieldFilter) (Filter, error) {
	op, ok := fieldOps[ff.GetOp()]
	if !ok {
		return Filter{}, errUnknownOperator
	}
	p, err := fieldPathOf(ff.GetField())
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
			return Filter{}, errNeedsArray(op)
		}
		out.Values = v.Elements()
		if len(out.Values) == 0 {
			return Filter{}, errNeedsArray(op)
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
		if err := checkNameOperand(op, out); err != nil {
			return Filter{}, err
		}
	}
	return out, nil
}

// checkNameOperand checks a filter on __name__: its operands are references, and it is a
// comparison, an equality or a membership, never a containment or a unary test.
func checkNameOperand(op index.Operator, f Filter) error {
	switch op {
	case index.OpArrayContains, index.OpArrayContainsAny:
		return errNameArrayFilter
	case index.OpIn, index.OpNotIn:
		for _, e := range f.Values {
			if e.Kind() != value.KindReference {
				return errNameNotReference
			}
		}
		return nil
	}
	if f.Value.Kind() != value.KindReference {
		return errNameNotReference
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
