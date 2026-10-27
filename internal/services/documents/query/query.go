package query

import (
	"github.com/arumadev/aruma/internal/services/documents/index"
	"github.com/arumadev/aruma/internal/services/documents/value"
	"github.com/arumadev/aruma/internal/services/documents/wire"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// Query is a StructuredQuery as the engine runs it, checked as production checks it.
type Query struct {
	// CollectionID is the from selector's collection id; "" for a kindless query, which reads
	// every collection (directly under the parent, or below it with AllDescendants).
	CollectionID string
	// AllDescendants reads the collections of that id anywhere below the parent (a collection
	// group query), not only those directly under it.
	AllDescendants bool
	// Where is the filter; nil for none.
	Where *Filter
	// Explicit is the order as sent; Order the whole order the query runs in (order.go), which
	// ends with __name__.
	Explicit []Order
	Order    []Order
	// Projection is the fields a result keeps, when Projected: __name__ alone keeps none.
	Projection []value.FieldPath
	Projected  bool
	Start, End *Cursor
	Offset     int
	// Limit is the most results; -1 for no limit.
	Limit int
	// FindNearest is the vector search, or nil.
	FindNearest *FindNearest
}

// Parse checks a StructuredQuery under a parent, as production checks it, and returns the
// query it runs. parent is the request's parent, the documents root or a document. A failure
// is a *wire.Error with production's text, or a provisional one (validate.go).
func Parse(sq *pb.StructuredQuery, parent wire.Name) (*Query, error) {
	if sq == nil {
		return nil, errNoQuery
	}
	if len(sq.GetFrom()) != 1 {
		return nil, errNoFrom
	}
	from := sq.GetFrom()[0]
	q := &Query{CollectionID: from.GetCollectionId(), AllDescendants: from.GetAllDescendants(), Limit: -1}
	if q.CollectionID != "" {
		if err := wire.CheckID(q.CollectionID, false); err != nil {
			return nil, err
		}
	}
	if sq.GetWhere() != nil {
		f, err := filterOf(sq.GetWhere())
		if err != nil {
			return nil, err
		}
		q.Where = &f
	}
	var err error
	if q.Explicit, err = ordersOf(sq.GetOrderBy()); err != nil {
		return nil, err
	}
	if q.CollectionID == "" || q.AllDescendants && !parent.IsRoot() {
		if err := checkKindless(q, parent); err != nil {
			return nil, err
		}
	}
	if q.Order, err = normalizeOrder(q.Explicit, q.Where); err != nil {
		return nil, err
	}
	if err := checkFilter(q.Where, len(q.Order), len(parent.Path)); err != nil {
		return nil, err
	}
	if sel := sq.GetSelect(); sel != nil && len(sel.GetFields()) > 0 {
		q.Projected = true
		for _, r := range sel.GetFields() {
			p, err := fieldPathOf(r)
			if err != nil {
				return nil, err
			}
			if err := value.ValidateMaskPath(p); err != nil {
				return nil, wire.InvalidArgument(err.Error())
			}
			if !p.IsDocumentID() {
				q.Projection = append(q.Projection, p)
			}
		}
	}
	if q.Start, err = cursorOf(sq.GetStartAt(), q.Order); err != nil {
		return nil, err
	}
	if q.End, err = cursorOf(sq.GetEndAt(), q.Order); err != nil {
		return nil, err
	}
	if sq.GetOffset() < 0 {
		return nil, errNegativeOffset
	}
	q.Offset = int(sq.GetOffset())
	if l := sq.GetLimit(); l != nil {
		if l.GetValue() < 0 {
			return nil, errNegativeLimit
		}
		q.Limit = int(l.GetValue())
	}
	if sq.GetFindNearest() != nil {
		if q.FindNearest, err = findNearestOf(sq.GetFindNearest()); err != nil {
			return nil, err
		}
	}
	return q, nil
}

// checkKindless checks a query without a collection id, or a collection group query under a
// document: production serves them from the key alone. Under a document, a filter or an order
// on a field is "collection group queries are only allowed at the root parent"
// (f0-descendants.json rest.kindless_with_field_filter, rest.kindless_order_by_field); at the
// root an order other than __name__ ascending is "kind is required for all orders except
// __key__ ascending", as ListDocuments answers it (f1-list-documents.json; provisional for a
// query). A filter on a field at the root is served (provisional: production's answer is not
// recorded), and package index logs it as unprobed.
func checkKindless(q *Query, parent wire.Name) error {
	onField := q.Where != nil && hasFieldFilter(q.Where)
	ordered := false
	for _, o := range q.Explicit {
		if !o.Path.IsDocumentID() || o.Desc {
			ordered = true
		}
	}
	switch {
	case !parent.IsRoot() && q.AllDescendants && (onField || ordered):
		return errGroupNotAtRoot
	case q.CollectionID == "" && ordered:
		return errKindlessOrder
	}
	return nil
}

// hasFieldFilter reports whether the tree filters on a field other than __name__.
func hasFieldFilter(f *Filter) bool {
	found := false
	walk(f, func(l *Filter) { found = found || !l.composite() && !l.Path.IsDocumentID() })
	return found
}

// Shape is the query as production's index rule reads it (index.QueryShape): the from
// selector, the filter's operators and the explicit order. aggs are a
// StructuredAggregationQuery's, nil for a plain query.
func (q *Query) Shape(aggs []index.Aggregation) index.QueryShape {
	s := index.QueryShape{CollectionID: q.CollectionID, Scope: index.ScopeCollection, Aggregations: aggs}
	if q.AllDescendants {
		s.Scope = index.ScopeCollectionGroup
	}
	if q.Where != nil {
		w := shapeFilter(q.Where)
		s.Where = &w
	}
	for _, o := range q.Explicit {
		s.OrderBy = append(s.OrderBy, index.Order{Field: o.Path.String(), Desc: o.Desc})
	}
	return s
}

// Project returns the fields a result of q keeps of fields.
func (q *Query) Project(fields value.Value) value.Value {
	if !q.Projected {
		return fields
	}
	return value.Project(fields, q.Projection)
}
