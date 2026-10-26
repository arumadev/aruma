package index

// QueryShape is everything about a query that production's index rule reads, and nothing else.
// The query package builds it from a StructuredQuery (or the query under a
// StructuredAggregationQuery) after validation; values never matter (R14: the verdict is the
// same on a populated and on a never-written collection), so none are carried.
//
// Left out on purpose, because the probe shows they never change the verdict:
//   - projection, offset and limit (R4: select_eq, offset_eq, range_ord_same_limit);
//   - cursors (cursor_single, cursor_eq_name, cursor_eq_other, eq_ord_name_cursor). A cursor
//     only bounds the scan of the index the orders already require;
//   - limitToLast, which never reaches the server: the SDK sends the reversed orders, and those
//     are what OrderBy holds (R17).
type QueryShape struct {
	// CollectionID is the collection id of the query's single from selector. Empty with
	// ScopeCollectionGroup is a kindless all-descendants query (recursive delete), which only
	// the key index serves.
	CollectionID string
	// Scope is ScopeCollection for a query on one collection (allDescendants false) and
	// ScopeCollectionGroup for allDescendants true.
	Scope Scope
	// Where is the filter tree, nil for none.
	Where *Filter
	// OrderBy is the explicit order as sent, before any implicit order is added.
	OrderBy []Order
	// Aggregations is empty for a plain query. COUNT reads no field; SUM and AVG read Field.
	Aggregations []Aggregation
}

// Operator is a filter's operator: a field or unary filter's, or AND/OR for a composite.
type Operator uint8

// The operators of StructuredQuery.FieldFilter.Operator, UnaryFilter.Operator and
// CompositeFilter.Operator.
const (
	OpUnspecified Operator = iota
	OpEqual
	OpIn
	OpArrayContains
	OpArrayContainsAny
	OpIsNull
	OpIsNaN
	OpLessThan
	OpLessThanOrEqual
	OpGreaterThan
	OpGreaterThanOrEqual
	OpNotEqual
	OpNotIn
	OpIsNotNull
	OpIsNotNaN
	OpAnd
	OpOr
)

// equalityLike is true for the operators an index serves with a fixed prefix: ==, in,
// array-contains, array-contains-any, IS_NULL and IS_NAN (the evidence's "equality-like").
func (op Operator) equalityLike() bool {
	switch op {
	case OpEqual, OpIn, OpArrayContains, OpArrayContainsAny, OpIsNull, OpIsNaN:
		return true
	}
	return false
}

// inequality is true for the operators that imply an order on their field: <, <=, >, >=, !=,
// not-in, IS_NOT_NULL and IS_NOT_NAN.
func (op Operator) inequality() bool {
	switch op {
	case OpLessThan, OpLessThanOrEqual, OpGreaterThan, OpGreaterThanOrEqual, OpNotEqual, OpNotIn,
		OpIsNotNull, OpIsNotNaN:
		return true
	}
	return false
}

// contains is true for the operators an array-contains index serves.
func (op Operator) contains() bool { return op == OpArrayContains || op == OpArrayContainsAny }

// Filter is one node of the filter tree: a field or unary filter (Field and Op), or a composite
// (Op is OpAnd or OpOr, with Filters).
type Filter struct {
	Op      Operator
	Field   string
	Filters []Filter
}

// Where is a field or unary filter on field.
func Where(field string, op Operator) Filter { return Filter{Op: op, Field: field} }

// And is the conjunction of filters.
func And(filters ...Filter) Filter { return Filter{Op: OpAnd, Filters: filters} }

// Or is the disjunction of filters.
func Or(filters ...Filter) Filter { return Filter{Op: OpOr, Filters: filters} }

// Order is one explicit orderBy.
type Order struct {
	Field string
	Desc  bool
}

// AggregationOp is an aggregation's function.
type AggregationOp uint8

// The aggregations of StructuredAggregationQuery.
const (
	AggCount AggregationOp = iota + 1
	AggSum
	AggAvg
)

// Aggregation is one aggregation. Field is empty for AggCount.
type Aggregation struct {
	Op    AggregationOp
	Field string
}
