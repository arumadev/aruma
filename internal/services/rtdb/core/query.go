package core

import (
	"encoding/json"
	"slices"
)

// The SDKs' sentinel key names: a bound with MinName starts before every key with its
// value, MaxName ends after every one (util/util.ts MIN_NAME, MAX_NAME). startAfter on a
// value sends MaxName as the start key, endBefore sends MinName as the end key
// (QueryParams.ts queryParamsStartAfter, queryParamsEndBefore).
const (
	MinName = "[MIN_NAME]"
	MaxName = "[MAX_NAME]"
)

// QuerySpec is a query as the protocol carries it (the q object of q, n and g, and later the
// REST parameters): raw JSON values, names and flags, read by NewQuery.
type QuerySpec struct {
	Index          string // "" or ".priority", ".key", ".value", or a child path
	StartValue     json.RawMessage
	StartName      *string
	StartInclusive *bool
	EndValue       json.RawMessage
	EndName        *string
	EndInclusive   *bool
	Limit          int64
	FromRight      bool // limitToLast
}

// Query selects children of a node in an index order: a range of index values (with keys
// breaking ties), then the first or last Limit of them, as the SDKs' RangedFilter and
// LimitedFilter do (core/view/filter). The server applies it so a client receives the
// window it asked for; the client re-applies it to everything it receives.
type Query struct {
	Index      Index
	start, end *bound
	Limit      int
	FromRight  bool
	// spec is the canonical text of the query, which identifies a listen.
	spec string
}

// bound is one end of a query's range: an index value and a key, the SDKs' "post".
type bound struct {
	value     *Node
	name      string
	inclusive bool
}

// DefaultQuery selects the whole node, in priority order.
var DefaultQuery = Query{Index: ByPriority, spec: "{}"}

// NewQuery reads a query as the protocol carries it.
func NewQuery(s QuerySpec) (Query, error) {
	q := Query{Index: ByPriority}
	switch s.Index {
	case "", ".priority":
	case ".key":
		q.Index = ByKey
	case ".value":
		q.Index = ByValue
	default:
		p, err := ParsePath(s.Index)
		if err != nil || p.IsRoot() {
			return Query{}, ErrInvalidPath
		}
		q.Index = ByChild(p)
	}
	var err error
	if q.start, err = q.newBound(s.StartValue, s.StartName, s.StartInclusive, MinName); err != nil {
		return Query{}, err
	}
	if q.end, err = q.newBound(s.EndValue, s.EndName, s.EndInclusive, MaxName); err != nil {
		return Query{}, err
	}
	if s.Limit < 0 {
		return Query{}, ErrInvalidLimit
	}
	q.Limit, q.FromRight = int(s.Limit), s.FromRight && s.Limit > 0
	q.spec = s.canonical()
	return q, nil
}

func (q Query) newBound(raw json.RawMessage, name *string, inclusive *bool, defaultName string) (*bound, error) {
	if raw == nil {
		return nil, nil
	}
	v, err := ParseData(raw, nil, 1<<20)
	if err != nil || v.HasServerValues() || v.Kind() == KindObject {
		return nil, ErrInvalidQuery
	}
	b := &bound{value: v, name: defaultName, inclusive: true}
	if name != nil {
		b.name = *name
	}
	if inclusive != nil {
		b.inclusive = *inclusive
	}
	if q.Index.kind == byKey && v.Kind() != KindString {
		return nil, ErrInvalidQuery
	}
	return b, nil
}

// canonical writes the spec in one fixed form, so the same query always reads the same.
func (s QuerySpec) canonical() string {
	m := map[string]any{}
	if s.Index != "" && s.Index != ".priority" {
		m["i"] = s.Index
	}
	if s.StartValue != nil {
		m["sp"] = json.RawMessage(s.StartValue)
		if s.StartName != nil {
			m["sn"] = *s.StartName
		}
		if s.StartInclusive != nil && !*s.StartInclusive {
			m["sin"] = false
		}
	}
	if s.EndValue != nil {
		m["ep"] = json.RawMessage(s.EndValue)
		if s.EndName != nil {
			m["en"] = *s.EndName
		}
		if s.EndInclusive != nil && !*s.EndInclusive {
			m["ein"] = false
		}
	}
	if s.Limit > 0 {
		m["l"] = s.Limit
		m["vf"] = "l"
		if s.FromRight {
			m["vf"] = "r"
		}
	}
	b, _ := json.Marshal(m) // map keys are sorted
	return string(b)
}

// ID identifies the query among a connection's listens at one path.
func (q Query) ID() string {
	if q.spec == "" {
		return "{}"
	}
	return q.spec
}

// IsDefault reports whether the query selects the whole node in priority order.
func (q Query) IsDefault() bool { return q.LoadsAll() && q.Index.kind == byPriority }

// LoadsAll reports whether the query keeps every child: no range and no limit.
func (q Query) LoadsAll() bool { return q.start == nil && q.end == nil && q.Limit == 0 }

// IndexKey is the .indexOn entry a query needs, "" for key and priority order, which need
// none.
func (q Query) IndexKey() string {
	switch q.Index.kind {
	case byValue:
		return ".value"
	case byChild:
		return q.Index.child.String()
	}
	return ""
}

// Keys returns the children of n the query selects, in index order. A leaf has none. With a
// limit, only the selected children are put in order, not every child.
func (q Query) Keys(n *Node) []string {
	es := n.sortEntries(q.Index)
	if q.start != nil || q.end != nil {
		es = slices.DeleteFunc(es, func(e sortEntry) bool { return !q.inRange(e.key, e.child) })
	}
	switch {
	case q.Limit <= 0 || len(es) <= q.Limit:
		slices.SortFunc(es, q.Index.compareEntries)
	case q.FromRight:
		es = smallest(es, q.Limit, func(a, b sortEntry) int { return q.Index.compareEntries(b, a) })
		slices.Reverse(es)
	default:
		es = smallest(es, q.Limit, q.Index.compareEntries)
	}
	return entryKeys(es)
}

// Filter returns the part of n the query selects: n itself when it loads everything, else
// an object of the selected children. A query result carries no priority of its own
// (RangedFilter.updateFullNode).
func (q Query) Filter(n *Node) *Node {
	if q.LoadsAll() {
		return n
	}
	keys := q.Keys(n)
	children := make(map[string]*Node, len(keys))
	for _, k := range keys {
		children[k] = n.children[k]
	}
	return Object(children)
}

// Update returns the query's window over n from old, its window over n before a change to
// only the children keys, without visiting n's other children. old must be Filter's result
// on n as it was before the change. It reports false when it cannot tell: a child the limit
// left out of old may now belong in the window, which only Filter can find.
func (q Query) Update(old, n *Node, keys []string) (*Node, bool) {
	if q.LoadsAll() {
		return n, true
	}
	// first orders the window's children: those a limit keeps come first.
	first := q.Index.compareEntries
	if q.FromRight {
		first = func(a, b sortEntry) int { return q.Index.compareEntries(b, a) }
	}
	// A full window may have left in-range children out, unchanged since: all of them come
	// after its last entry, its edge. A child that now comes before the edge is sure to be
	// ahead of them; one after it may not be.
	full := q.Limit > 0 && old.Len() >= q.Limit
	var edge sortEntry
	changed := make(map[string]bool, len(keys))
	for _, k := range keys {
		changed[k] = true
	}
	es := make([]sortEntry, 0, old.Len()+len(keys))
	if old != nil {
		for k, c := range old.children {
			e := newSortEntry(q.Index, k, c)
			if full && (edge.child == nil || first(e, edge) > 0) {
				edge = e
			}
			if !changed[k] {
				es = append(es, e)
			}
		}
	}
	for k := range changed {
		c := n.Child(k)
		if c == nil || !q.inRange(k, c) {
			continue
		}
		if e := newSortEntry(q.Index, k, c); !full || first(e, edge) <= 0 {
			es = append(es, e)
		}
	}
	if full && len(es) < q.Limit {
		return nil, false
	}
	if q.Limit > 0 {
		es = smallest(es, q.Limit, first)
	}
	children := make(map[string]*Node, len(es))
	for _, e := range es {
		children[e.key] = e.child
	}
	return Object(children), true
}

// inRange reports whether the child key with value c lies within the query's bounds.
func (q Query) inRange(key string, c *Node) bool {
	if b := q.start; b != nil {
		if cmp := q.compare(key, c, b); cmp < 0 || cmp == 0 && !b.inclusive {
			return false
		}
	}
	if b := q.end; b != nil {
		if cmp := q.compare(key, c, b); cmp > 0 || cmp == 0 && !b.inclusive {
			return false
		}
	}
	return true
}

// compare orders the child (key, c) against a bound, as Index.compare orders a NamedNode
// against the bound's post.
func (q Query) compare(key string, c *Node, b *bound) int {
	if q.Index.kind == byKey {
		return compareNames(key, b.value.StringValue())
	}
	if cmp := CompareValues(q.Index.value(c), b.value); cmp != 0 {
		return cmp
	}
	return compareNames(key, b.name)
}

// compareNames is nameCompare with the sentinel names.
func compareNames(a, b string) int {
	switch {
	case a == b:
		return 0
	case a == MinName || b == MaxName:
		return -1
	case b == MinName || a == MaxName:
		return 1
	}
	return CompareKeys(a, b)
}
