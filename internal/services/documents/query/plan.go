package query

import (
	"math"
	"slices"

	"github.com/arumadev/aruma/internal/services/documents/index"
	"github.com/arumadev/aruma/internal/services/documents/value"
)

// The planner (ADR-025 §1): a query served from index entries when an index serves it exactly,
// the reference path otherwise. Every shape it refuses is a performance opportunity, never a
// correctness risk: Plan answers ok false and the query takes the reference path, which serves
// everything.
//
// A plan is streams of candidates, each in the query's whole order, merged in that order once
// each document (a union, for an OR, an in or an array-contains-any). A stream is one range of
// one index, or the intersection by name of equality ranges of several (index_merged, a
// zig-zag join). A range is the entries that start with the index's prefix and the values of
// its leading equality fields, bounded on the field after them by the range filters on it and by
// the cursors. Every candidate is checked against the filters its range claims to hold
// (recheck.go), then by the reference path's runner against the whole query: the remaining
// filters, the cursors, the offset and the limit.
//
// An index serves a conjunction when its fields are, in order:
//
//   - fields each held to one value by an ==, IS_NULL or IS_NAN on it (ascending or descending
//     alike), or an array-contains field held by an array-contains on it;
//   - then the query's whole order without the fields held to one value, each in its direction,
//     ending with __name__.
//
// The single-field indexes are such indexes of one field and __name__ (ascending and
// array-contains before __name__ ascending, descending before __name__ descending, automatic.go
// in package index), and so is a composite index. Only READY indexes serve. Equality filters an
// index does not hold, a second range field, !=, not-in and the IS_NOT tests are left to the
// runner. The planner refuses an operand that matches nothing or matches by a quirk (null or
// NaN under ==, a range or array-contains), a kindless query, and a disjunct no index serves.

// Plan is how the planner serves a query.
type Plan struct {
	// Path is the counter the plan is served under (paths.go).
	Path Path
	// Streams are each in the query's order; their union, once each document, is a superset of
	// the query's results.
	Streams []Stream
}

// Stream is one source of candidates in the query's order: one Scan, or more intersected by
// name, each then in name order.
type Stream struct {
	Scans []Scan
	// Conjunction is the disjunct of the query's filter the stream serves: a candidate that
	// does not match it is left out before the stream's order is checked, as only the
	// documents that match it come in the query's order (the fields it holds to one value
	// vary among the others).
	Conjunction []*Filter
}

// Scan is a range of one index: the entries whose keys start with the index's prefix and Prefix,
// at or past every Lo and before every Hi.
type Scan struct {
	// Index is the index scanned, normalized; Single is a single-field index (its field and
	// __name__), else a composite.
	Index  index.Index
	Single bool
	// Prefix are the parts of the index's leading fields held to one value.
	Prefix []Part
	// Lo and Hi bound the entries past Prefix: an entry is in when its key is at or past every
	// Lo and before every Hi.
	Lo, Hi []Bound
	// Exact are the filters every entry of the range holds: a candidate that does not match one
	// is a recheck mismatch (recheck.go).
	Exact []*Filter
}

// Part is one field's part of a key: a value's (or, for __name__, a document name's, Value
// being its reference), or a range class's edge, inverted for a descending field.
type Part struct {
	Value value.Value
	// Edge is a class edge (value.ClassBounds without its version byte) in place of Value.
	Edge []byte
	Name bool
	Desc bool
}

// Bound is a position among an index's entries past a scan's prefix: before the first entry that
// starts with Parts, or with After past the last of them.
type Bound struct {
	Parts []Part
	After bool
}

// Plan returns the plan that serves q from the indexes of cat, the planner's catalogue at the
// read: ok is false for a query only the reference path serves.
func (q *Query) Plan(cat *index.Catalog) (*Plan, bool) {
	if q.CollectionID == "" || q.FindNearest != nil || q.Limit == 0 {
		return nil, false
	}
	scope := index.ScopeCollection
	if q.AllDescendants {
		scope = index.ScopeCollectionGroup
	}
	p := &planner{q: q, cat: cat, group: q.CollectionID, scope: scope}
	conjs := [][]*Filter{nil}
	if q.Where != nil {
		conjs = nil
		for _, c := range disjuncts(q.Where) {
			conjs = append(conjs, expandMembers(c)...)
		}
	}
	plan := &Plan{Path: PathIndex}
	for _, c := range conjs {
		s, ok := p.conjunction(c)
		if !ok {
			return nil, false
		}
		plan.Streams = append(plan.Streams, s)
		for _, sc := range s.Scans {
			if len(s.Scans) > 1 {
				plan.Path = PathIndexMerged
			} else if plan.Path == PathIndex && sc.contains() {
				plan.Path = PathIndexContains
			}
		}
	}
	return plan, true
}

// contains reports whether a scan reads an array-contains index.
func (s *Scan) contains() bool {
	return slices.ContainsFunc(s.Index.Fields, func(f index.IndexField) bool { return f.Mode == index.Contains })
}

// expandMembers expands a conjunction's in and array-contains-any into one conjunction per
// value: == and array-contains leaves of their own.
func expandMembers(conj []*Filter) [][]*Filter {
	out := [][]*Filter{nil}
	for _, l := range conj {
		var alts []*Filter
		switch l.Op {
		case index.OpIn:
			for _, v := range l.Values {
				alts = append(alts, &Filter{Op: index.OpEqual, Path: l.Path, Value: v})
			}
		case index.OpArrayContainsAny:
			for _, v := range l.Values {
				alts = append(alts, &Filter{Op: index.OpArrayContains, Path: l.Path, Value: v})
			}
		default:
			alts = []*Filter{l}
		}
		next := make([][]*Filter, 0, len(out)*len(alts))
		for _, c := range out {
			for _, a := range alts {
				next = append(next, append(slices.Clip(c), a))
			}
		}
		out = next
	}
	return out
}

type planner struct {
	q     *Query
	cat   *index.Catalog
	group string
	scope index.Scope
}

// conjunct is a conjunction's leaves as the planner reads them.
type conjunct struct {
	// held are the == (operand neither null nor NaN), IS_NULL and IS_NAN leaves, by the value
	// they hold their field to; contains the array-contains leaves (operand neither null nor
	// NaN).
	held     []heldLeaf
	contains []heldLeaf
	// ranges are the range leaves by field.
	ranges map[string][]*Filter
	// names are the range leaves on __name__.
	names []*Filter
	// tail is the query's order without the fields held to one value.
	tail []Order
}

type heldLeaf struct {
	leaf  *Filter
	path  string
	value value.Value
}

// conjunction plans one conjunction: the best single index, or an intersection of single-field
// equality ranges in name order.
func (p *planner) conjunction(leaves []*Filter) (Stream, bool) {
	c, ok := p.read(leaves)
	if !ok {
		return Stream{}, false
	}
	best, bestScore := Scan{}, -1
	for _, ix := range p.cat.ReadyComposites(p.group, p.scope) {
		if s, score, ok := p.scanOf(c, ix, false); ok && score > bestScore {
			best, bestScore = s, score
		}
	}
	for _, ix := range p.singleFields(c) {
		if s, score, ok := p.scanOf(c, ix, true); ok && score > bestScore {
			best, bestScore = s, score
		}
	}
	if merged, ok := p.intersection(c); ok && 2*len(merged.Scans) > bestScore {
		merged.Conjunction = leaves
		return merged, true
	}
	if bestScore < 0 {
		return Stream{}, false
	}
	return Stream{Scans: []Scan{best}, Conjunction: leaves}, true
}

// read sorts a conjunction's leaves, refusing an operand that matches nothing or by a quirk.
func (p *planner) read(leaves []*Filter) (conjunct, bool) {
	c := conjunct{ranges: map[string][]*Filter{}}
	held := map[string]bool{}
	for _, l := range leaves {
		path := l.Path.String()
		switch l.Op {
		case index.OpEqual, index.OpArrayContains:
			if l.Value.IsNull() || l.Value.IsNaN() {
				return conjunct{}, false
			}
			if l.Path.IsDocumentID() {
				continue
			}
			h := heldLeaf{leaf: l, path: path, value: l.Value}
			if l.Op == index.OpEqual {
				c.held = append(c.held, h)
				held[path] = true
			} else {
				c.contains = append(c.contains, h)
			}
		case index.OpIsNull:
			c.held = append(c.held, heldLeaf{leaf: l, path: path, value: value.Null()})
			held[path] = true
		case index.OpIsNaN:
			c.held = append(c.held, heldLeaf{leaf: l, path: path, value: value.Double(math.NaN())})
			held[path] = true
		case index.OpLessThan, index.OpLessThanOrEqual, index.OpGreaterThan, index.OpGreaterThanOrEqual:
			if l.Value.IsNull() || l.Value.IsNaN() {
				return conjunct{}, false
			}
			if l.Path.IsDocumentID() {
				c.names = append(c.names, l)
			} else {
				c.ranges[path] = append(c.ranges[path], l)
			}
		}
	}
	for _, o := range p.q.Order {
		if !held[o.Path.String()] {
			c.tail = append(c.tail, o)
		}
	}
	return c, true
}

// singleFields are the READY single-field indexes that could serve c alone: on its field held
// or contained when it is ordered by __name__ alone, or on its first order otherwise.
func (p *planner) singleFields(c conjunct) []index.Index {
	var out []index.Index
	add := func(path string, mode index.FieldMode) {
		if !p.cat.SingleFieldReady(p.group, path, p.scope, mode) {
			return
		}
		out = append(out, singleFieldIndex(p.group, p.scope, path, mode))
	}
	if len(c.tail) == 1 {
		desc := c.tail[0].Desc
		for _, h := range c.held {
			if desc {
				add(h.path, index.Descending)
			} else {
				add(h.path, index.Ascending)
			}
		}
		if !desc {
			for _, h := range c.contains {
				add(h.path, index.Contains)
			}
		}
		return out
	}
	if len(c.tail) == 2 && c.tail[1].Desc == c.tail[0].Desc {
		if c.tail[0].Desc {
			add(c.tail[0].Path.String(), index.Descending)
		} else {
			add(c.tail[0].Path.String(), index.Ascending)
		}
	}
	return out
}

func singleFieldIndex(group string, scope index.Scope, path string, mode index.FieldMode) index.Index {
	return index.Index{CollectionGroup: group, Scope: scope, Fields: []index.IndexField{{Path: path, Mode: mode}}}.Normalize()
}

// scanOf returns the scan of ix that serves c, and its score: twice the equality filters it
// holds, and one for a range on the field after them. ok is false when ix does not serve c.
func (p *planner) scanOf(c conjunct, ix index.Index, single bool) (Scan, int, bool) {
	s := Scan{Index: ix, Single: single}
	usedHeld := make([]bool, len(c.held))
	usedContains := make([]bool, len(c.contains))
	k := 0
	for _, f := range ix.Fields[:len(ix.Fields)-1] {
		found := false
		if f.Mode == index.Contains {
			if i := unused(c.contains, usedContains, f.Path); i >= 0 {
				usedContains[i], found = true, true
				s.Prefix = append(s.Prefix, Part{Value: c.contains[i].value})
				s.Exact = append(s.Exact, c.contains[i].leaf)
			}
		} else if i := unused(c.held, usedHeld, f.Path); i >= 0 {
			usedHeld[i], found = true, true
			s.Prefix = append(s.Prefix, Part{Value: c.held[i].value, Desc: f.Mode == index.Descending})
			s.Exact = append(s.Exact, c.held[i].leaf)
		}
		if !found {
			break
		}
		k++
	}
	rest := ix.Fields[k:]
	if len(rest) != len(c.tail) {
		return Scan{}, 0, false
	}
	for i, f := range rest {
		o := c.tail[i]
		if f.Mode == index.Contains || f.Path != o.Path.String() || (f.Mode == index.Descending) != o.Desc {
			return Scan{}, 0, false
		}
	}
	score := 2 * k
	first := c.tail[0]
	if first.Path.IsDocumentID() {
		for _, l := range c.names {
			s.addRange(l, true, first.Desc)
		}
	} else if rs := c.ranges[first.Path.String()]; len(rs) > 0 {
		for _, l := range rs {
			s.addRange(l, false, first.Desc)
			s.Exact = append(s.Exact, l)
		}
		score++
	}
	s.addCursors(p.q, c.tail, rest)
	return s, score, true
}

// unused returns the first leaf of ls on path not used yet, or -1.
func unused(ls []heldLeaf, used []bool, path string) int {
	for i, l := range ls {
		if !used[i] && l.path == path {
			return i
		}
	}
	return -1
}

// addRange bounds the scan by a range leaf on the field after its prefix, desc when that field
// descends. A field's range spans its operand's class (value.InRange); a range on __name__ is a
// name's.
func (s *Scan) addRange(l *Filter, name, desc bool) {
	at := func(after bool) Bound {
		return Bound{Parts: []Part{{Value: l.Value, Name: name, Desc: desc}}, After: after}
	}
	lower := l.Op == index.OpGreaterThan || l.Op == index.OpGreaterThanOrEqual
	inclusive := l.Op == index.OpGreaterThanOrEqual || l.Op == index.OpLessThanOrEqual
	// Ascending, a lower bound on the value is a lower bound on the keys; descending, an upper.
	// Either way the keys of the operand's ties are in when the bound is inclusive.
	if lower != desc {
		s.Lo = append(s.Lo, at(!inclusive))
	} else {
		s.Hi = append(s.Hi, at(inclusive))
	}
	if name {
		return
	}
	lo, hi, _ := value.ClassBounds(l.Value)
	edge := func(e []byte, after bool) Bound { return Bound{Parts: []Part{{Edge: e[1:], Desc: desc}}, After: after} }
	if desc {
		s.Hi = append(s.Hi, edge(lo, true))
		s.Lo = append(s.Lo, edge(hi, true))
	} else {
		s.Lo = append(s.Lo, edge(lo, false))
		s.Hi = append(s.Hi, edge(hi, false))
	}
}

// addCursors bounds a scan whose fields past its prefix are rest, the conjunction's tail of the
// order, by the query's cursors: those whose values cover no field held to one value, so that
// they are values for rest's first fields. The others are the runner's alone.
func (s *Scan) addCursors(q *Query, tail []Order, rest []index.IndexField) {
	bound := func(c *Cursor) (Bound, bool) {
		if c == nil || len(c.Values) == 0 || len(c.Values) > len(tail) {
			return Bound{}, false
		}
		for i := range c.Values {
			if !slices.Equal(q.Order[i].Path, tail[i].Path) {
				return Bound{}, false
			}
		}
		// start_at and end_before (before) are at the tied entries, start_after and end_at
		// past them.
		b := Bound{Parts: make([]Part, len(c.Values)), After: !c.Before}
		for i, v := range c.Values {
			b.Parts[i] = Part{Value: v, Name: rest[i].Path == index.NameField, Desc: rest[i].Mode == index.Descending}
		}
		return b, true
	}
	if b, ok := bound(q.Start); ok {
		s.Lo = append(s.Lo, b)
	}
	if b, ok := bound(q.End); ok {
		s.Hi = append(s.Hi, b)
	}
}

// intersection plans c as the intersection of single-field equality ranges in name order, when
// c is ordered by __name__ alone and at least two of its equalities have such an index; the
// equalities without one are left to the runner.
func (p *planner) intersection(c conjunct) (Stream, bool) {
	if len(c.tail) != 1 || len(c.held)+len(c.contains) < 2 {
		return Stream{}, false
	}
	desc := c.tail[0].Desc
	var st Stream
	add := func(h heldLeaf, mode index.FieldMode) {
		if !p.cat.SingleFieldReady(p.group, h.path, p.scope, mode) {
			return
		}
		ix := singleFieldIndex(p.group, p.scope, h.path, mode)
		s := Scan{Index: ix, Single: true, Prefix: []Part{{Value: h.value, Desc: mode == index.Descending}},
			Exact: []*Filter{h.leaf}}
		rest := ix.Fields[1:]
		for _, l := range c.names {
			s.addRange(l, true, desc)
		}
		s.addCursors(p.q, c.tail, rest)
		st.Scans = append(st.Scans, s)
	}
	for _, h := range c.held {
		if desc {
			add(h, index.Descending)
		} else {
			add(h, index.Ascending)
		}
	}
	if !desc {
		for _, h := range c.contains {
			add(h, index.Contains)
		}
	}
	return st, len(st.Scans) >= 2
}

// OnFields reports whether q filters or orders on a field other than __name__: a query an
// index could serve.
func (q *Query) OnFields() bool {
	return q.Where != nil && hasFieldFilter(q.Where) || !q.Order[0].Path.IsDocumentID()
}

// Compare orders two documents by q's whole order, as its results come: each has every field q
// orders by (a candidate the recheck passed).
func (q *Query) Compare(a, b *Doc) int {
	for _, o := range q.Order {
		va, _ := a.field(o.Path)
		vb, _ := b.field(o.Path)
		if c := o.compare(va, vb); c != 0 {
			return c
		}
	}
	return 0
}
