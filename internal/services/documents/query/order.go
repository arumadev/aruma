package query

import (
	"slices"

	"github.com/arumadev/aruma/internal/services/documents/value"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// Order is one field of a query's order.
type Order struct {
	Path value.FieldPath
	Desc bool
}

// compare orders two values of o's field in o's direction: a field's as its index does
// (value.Compare, which keeps 1500 bytes of a value), __name__ by the whole name, as the store
// orders documents (ADR-024 §1.4).
func (o Order) compare(a, b value.Value) int {
	c := value.Compare(a, b)
	if o.Path.IsDocumentID() {
		c = value.CompareWhole(a, b)
	}
	if o.Desc {
		c = -c
	}
	return c
}

// ordersOf reads the explicit orders off the wire. A direction left unspecified is ascending;
// one outside the enum is production's "Unrecognized ordering." (f1-wire-errors.json
// json.bad_enum_number).
func ordersOf(os []*pb.StructuredQuery_Order) ([]Order, error) {
	out := make([]Order, 0, len(os))
	for _, o := range os {
		p, err := fieldPathOf(o.GetField())
		if err != nil {
			return nil, err
		}
		var desc bool
		switch o.GetDirection() {
		case pb.StructuredQuery_DIRECTION_UNSPECIFIED, pb.StructuredQuery_ASCENDING:
		case pb.StructuredQuery_DESCENDING:
			desc = true
		default:
			return nil, errUnknownDirection
		}
		out = append(out, Order{Path: p, Desc: desc})
	}
	return out, nil
}

// normalizeOrder is the whole order a query runs in (query.proto, StructuredQuery.order_by;
// the SDKs' queryNormalizedOrderBy): the explicit orders; then every inequality field not
// ordered, in field path order, in the direction of the last explicit order (ascending without
// one); then __name__ in that direction unless ordered. A field ordered twice keeps its first
// place: the second compares what the first did.
//
// __name__ is the key and ends every index: an order after it is production's "order by clause
// cannot contain more fields after the key", which an explicit __name__ before an inequality
// field's implicit order meets too (f0-descendants.json rest.partition.with_filter).
func normalizeOrder(explicit []Order, where *Filter) ([]Order, error) {
	out := make([]Order, 0, len(explicit)+2)
	seen := func(p value.FieldPath) bool {
		return slices.ContainsFunc(out, func(o Order) bool { return slices.Equal(o.Path, p) })
	}
	named := false
	for _, o := range explicit {
		if named {
			return nil, errAfterKey
		}
		if seen(o.Path) {
			continue
		}
		out = append(out, o)
		named = o.Path.IsDocumentID()
	}
	lastDesc := len(explicit) > 0 && explicit[len(explicit)-1].Desc
	for _, p := range inequalityPaths(where) {
		if seen(p) {
			continue
		}
		if named {
			return nil, errAfterKey
		}
		out = append(out, Order{Path: p, Desc: lastDesc})
	}
	if !named {
		out = append(out, Order{Path: value.FieldPath{value.DocumentID}, Desc: lastDesc})
	}
	return out, nil
}

// byNameAsc reports whether an order is __name__ ascending alone: the order every scan of
// candidates returns them in, so the reference path streams it.
func byNameAsc(order []Order) bool {
	return len(order) == 1 && order[0].Path.IsDocumentID() && !order[0].Desc
}
