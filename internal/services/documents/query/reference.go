package query

import (
	"container/heap"
	"slices"

	"github.com/arumadev/aruma/internal/services/documents/index"
	"github.com/arumadev/aruma/internal/services/documents/value"
	"github.com/arumadev/aruma/internal/services/documents/wire"
)

// The reference path (ADR-025 §1): the candidates of a query, read in name order from its
// collection, its collection group or the descendants of its parent, are filtered and sorted
// here with value's comparison. It is the arbiter of every index path, and it serves what no
// index serves: reads at a read_time, kindless queries, and the enforcement mode off.
//
// Memory is bounded. A query ordered by __name__ ascending alone streams: each candidate that
// matches is sent as it is read, and the scan stops at the limit or past the end. Any other
// order keeps the matches until the scan ends, at most offset+limit of them in a heap when
// there is a limit, and within MaxKeptBytes of documents and MaxScanned candidates either way;
// past either the query fails with errScanBound, this server's own bound until the planner
// serves such queries from an index. An aggregation without cursors, offset or limit reads in
// any order and keeps nothing.

// Limits bound the reference path's work on a query that does not stream.
type Limits struct {
	// MaxScanned bounds the candidates read; 0 is DefaultMaxScanned.
	MaxScanned int
	// MaxKeptBytes bounds the documents kept for sorting, by value.DocumentSize; 0 is
	// DefaultMaxKeptBytes.
	MaxKeptBytes int
}

// The default bounds, those of ListDocuments with an order (api/listorder.go).
const (
	DefaultMaxScanned   = 100_000
	DefaultMaxKeptBytes = 256 << 20
)

var errScanBound = wire.NewError(wire.CodeResourceExhausted,
	"A query this server serves without an index reads at most 100000 documents and keeps at most 256 MiB of them; "+
		"order it by __name__ or narrow it.")

// Runner runs one query's reference path. Feed it the candidates in name order with Add until
// it reports done or they run out, then call Finish: results go to emit in the query's order,
// at most Limit of them past Offset.
type Runner struct {
	q         *Query
	emit      func(*Doc) error
	unordered bool
	limits    Limits

	stream  bool
	upper   *bound // the scan's upper bound on __name__, from the filter and the cursors
	skipped int
	sent    int
	scanned int

	// The matches kept when not streaming: a heap with the worst at the top when bounded by
	// k = offset+limit, else a plain list.
	kept      entries
	keptBytes int
	k         int
}

// entry is a kept match with the values of its order.
type entry struct {
	doc  *Doc
	vals []value.Value
}

// NewRunner returns a runner for q that sends results to emit. unordered is an aggregation's
// read, which needs no order when it has no cursor, offset or limit.
func NewRunner(q *Query, unordered bool, limits Limits, emit func(*Doc) error) *Runner {
	r := &Runner{q: q, emit: emit, limits: limits, k: -1}
	if r.limits.MaxScanned == 0 {
		r.limits.MaxScanned = DefaultMaxScanned
	}
	if r.limits.MaxKeptBytes == 0 {
		r.limits.MaxKeptBytes = DefaultMaxKeptBytes
	}
	r.unordered = unordered && q.Start == nil && q.End == nil && q.Offset == 0 && q.Limit < 0
	r.stream = r.unordered || byNameAsc(q.Order)
	if !r.stream && q.Limit >= 0 {
		r.k = q.Offset + q.Limit
	}
	_, r.upper = q.nameBounds()
	r.kept.order = q.Order
	return r
}

// NewOrderedRunner returns a runner for candidates that arrive in q's whole order, as an index
// path reads them (plan.go), rather than in name order: it streams, applies the filter, the
// cursors, the offset and the limit, and reports done at the limit or past the end cursor.
func NewOrderedRunner(q *Query, emit func(*Doc) error) *Runner {
	r := NewRunner(q, false, Limits{}, emit)
	r.stream, r.upper = true, nil
	return r
}

// Streams reports whether results are sent as candidates are added, before Finish.
func (r *Runner) Streams() bool { return r.stream }

// Skipped is how many matches the offset skipped.
func (r *Runner) Skipped() int { return r.skipped }

// Add considers one candidate, the next in name order, and reports whether the scan can stop:
// the limit is reached, or the candidate is past every bound on __name__.
func (r *Runner) Add(d *Doc) (done bool, err error) {
	if r.q.Limit == 0 && r.skipped >= r.q.Offset {
		// limit 0 still counts what the offset skips (f3-run-query.json limit.0_offset).
		return true, nil
	}
	if r.upper != nil && r.upper.past(value.Reference(d.Name)) {
		return true, nil
	}
	if r.q.Where != nil && !r.q.Where.Matches(d) {
		return false, r.count()
	}
	vals := make([]value.Value, len(r.q.Order))
	for i, o := range r.q.Order {
		v, ok := d.field(o.Path)
		if !ok {
			// A document without an ordered field is not in the result (ADR-025 §1).
			return false, r.count()
		}
		vals[i] = v
	}
	if !afterStart(vals, r.q.Order, r.q.Start) || !beforeEnd(vals, r.q.Order, r.q.End) {
		if r.stream && !r.unordered && !beforeEnd(vals, r.q.Order, r.q.End) {
			return true, nil // name ascending: every later candidate is past the end too
		}
		return false, r.count()
	}
	if r.stream {
		if r.skipped < r.q.Offset {
			r.skipped++
			return false, nil
		}
		if err := r.emit(r.projected(d)); err != nil {
			return true, err
		}
		r.sent++
		return r.q.Limit >= 0 && r.sent >= r.q.Limit, nil
	}
	if err := r.count(); err != nil {
		return true, err
	}
	return false, r.keep(entry{doc: r.projected(d), vals: vals})
}

// count counts a candidate the runner read and does not stream against MaxScanned.
func (r *Runner) count() error {
	if r.stream {
		return nil
	}
	if r.scanned++; r.scanned > r.limits.MaxScanned {
		return errScanBound
	}
	return nil
}

func (r *Runner) keep(e entry) error {
	if r.k == 0 {
		return nil
	}
	if r.k > 0 && len(r.kept.list) == r.k {
		// Full: e replaces the worst kept match if it comes before it.
		if r.kept.compare(e, r.kept.list[0]) >= 0 {
			return nil
		}
		r.keptBytes -= docBytes(r.kept.list[0].doc)
		r.kept.list[0] = e
		heap.Fix(&r.kept, 0)
	} else if r.k > 0 {
		heap.Push(&r.kept, e)
	} else {
		r.kept.list = append(r.kept.list, e)
	}
	if r.keptBytes += docBytes(e.doc); r.keptBytes > r.limits.MaxKeptBytes {
		return errScanBound
	}
	return nil
}

// Finish sends the kept matches, sorted, past the offset and within the limit.
func (r *Runner) Finish() error {
	if r.stream {
		return nil
	}
	list := r.kept.list
	slices.SortFunc(list, func(a, b entry) int { return r.kept.compare(a, b) })
	r.skipped = min(r.q.Offset, len(list))
	list = list[r.skipped:]
	if r.q.Limit >= 0 {
		list = list[:min(len(list), r.q.Limit)]
	}
	for _, e := range list {
		if err := r.emit(e.doc); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) projected(d *Doc) *Doc {
	if !r.q.Projected {
		return d
	}
	p := *d
	p.Fields = r.q.Project(d.Fields)
	return &p
}

func docBytes(d *Doc) int { return value.DocumentSize(d.Name, d.Fields) }

// entries is a max-heap of kept matches, the one that comes last in the order at the top.
type entries struct {
	order []Order
	list  []entry
}

// compare orders two matches by the query's order, each field in its direction.
func (h *entries) compare(a, b entry) int {
	for i, o := range h.order {
		if c := o.compare(a.vals[i], b.vals[i]); c != 0 {
			return c
		}
	}
	return 0
}

func (h *entries) Len() int           { return len(h.list) }
func (h *entries) Less(i, j int) bool { return h.compare(h.list[i], h.list[j]) > 0 }
func (h *entries) Swap(i, j int)      { h.list[i], h.list[j] = h.list[j], h.list[i] }
func (h *entries) Push(x any)         { h.list = append(h.list, x.(entry)) }
func (h *entries) Pop() any {
	e := h.list[len(h.list)-1]
	h.list = h.list[:len(h.list)-1]
	return e
}

// bound is a bound on __name__: a reference, and whether the reference itself is within it.
type bound struct {
	ref       value.Value
	inclusive bool
}

// past reports whether a name is above an upper bound.
func (b *bound) past(name value.Value) bool {
	c := value.CompareWhole(name, b.ref)
	return c > 0 || c == 0 && !b.inclusive
}

// NameBound is a bound on the names a query's scan reads: a document's full resource name, and
// whether that document is within it.
type NameBound struct {
	Name      string
	Inclusive bool
}

// Lower is where a scan in name order can start: the tightest lower bound on __name__ the
// filter's top-level conjuncts and, when the query is ordered by __name__ first, its cursors
// give. ok is false without one.
func (q *Query) Lower() (NameBound, bool) {
	lo, _ := q.nameBounds()
	if lo == nil {
		return NameBound{}, false
	}
	return NameBound{Name: lo.ref.StringValue(), Inclusive: lo.inclusive}, true
}

// nameBounds are the lower and upper bounds on __name__ the query gives: its top-level
// conjuncts on __name__ (<, <=, ==, >=, >), and its cursors when its first order is __name__
// (start below and end above when ascending, the reverse when descending).
func (q *Query) nameBounds() (lo, hi *bound) {
	tighten := func(cur *bound, b bound, lower bool) *bound {
		if cur == nil {
			return &b
		}
		c := value.CompareWhole(b.ref, cur.ref)
		if lower && c > 0 || !lower && c < 0 || c == 0 && !b.inclusive {
			return &b
		}
		return cur
	}
	var leaves []*Filter
	if q.Where != nil {
		leaves = []*Filter{q.Where}
		if q.Where.Op == index.OpAnd {
			leaves = leaves[:0]
			for i := range q.Where.Filters {
				leaves = append(leaves, &q.Where.Filters[i])
			}
		}
	}
	for _, l := range leaves {
		if !l.Path.IsDocumentID() || l.Value.Kind() != value.KindReference {
			continue
		}
		switch l.Op {
		case index.OpGreaterThan:
			lo = tighten(lo, bound{l.Value, false}, true)
		case index.OpGreaterThanOrEqual:
			lo = tighten(lo, bound{l.Value, true}, true)
		case index.OpLessThan:
			hi = tighten(hi, bound{l.Value, false}, false)
		case index.OpLessThanOrEqual:
			hi = tighten(hi, bound{l.Value, true}, false)
		case index.OpEqual:
			lo = tighten(lo, bound{l.Value, true}, true)
			hi = tighten(hi, bound{l.Value, true}, false)
		}
	}
	if len(q.Order) > 0 && q.Order[0].Path.IsDocumentID() {
		start, end := q.Start, q.End
		startLower := !q.Order[0].Desc
		if c := start; c != nil && len(c.Values) > 0 {
			// A one-value cursor on __name__ is the bound itself; a longer one ties on
			// __name__ only with the same document, so it bounds inclusively.
			b := bound{c.Values[0], c.Before || len(c.Values) > 1}
			if startLower {
				lo = tighten(lo, b, true)
			} else {
				hi = tighten(hi, b, false)
			}
		}
		if c := end; c != nil && len(c.Values) > 0 {
			b := bound{c.Values[0], !c.Before || len(c.Values) > 1}
			if startLower {
				hi = tighten(hi, b, false)
			} else {
				lo = tighten(lo, b, true)
			}
		}
	}
	return lo, hi
}
