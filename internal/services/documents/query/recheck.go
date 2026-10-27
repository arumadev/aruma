package query

import (
	"fmt"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/arumadev/aruma/internal/services/documents/value"
)

// The recheck (ADR-025 §1): every candidate an index path reads is checked again with the
// reference path's matcher before the runner sees it. A candidate the index claims and the
// matcher refuses is a bug in an index's entries or in the plan, never a row to drop: the query
// fails INTERNAL, and the mismatch is counted (documents_query_recheck_mismatch_total) and, by
// the caller, logged.
//
// A stream's candidates must also come in the query's whole order, which ends with __name__, so
// each comes strictly after the one before. Entries whose keys share their first 2,048 bytes are
// stored in hash order; the store orders each such run by the whole key before it returns any of
// it (core.ScanIndex, ADR-024 §1.4), so a limit, a cursor or a page never cuts a run unordered,
// and the order check here holds the store to it.

// mismatches counts the recheck mismatches since the process started.
var mismatches atomic.Int64

// MismatchCount is how many recheck mismatches there were since the process started.
func MismatchCount() int64 { return mismatches.Load() }

// mismatchCollector is documents_query_recheck_mismatch_total.
func mismatchCollector() prometheus.Collector {
	return prometheus.NewCounterFunc(prometheus.CounterOpts{
		Name: "documents_query_recheck_mismatch_total",
		Help: "Document Database index path candidates the reference matcher refused (each failed its query INTERNAL).",
	}, func() float64 { return float64(mismatches.Load()) })
}

// RecheckError is a recheck mismatch: the query fails INTERNAL.
type RecheckError struct {
	Name   string
	Reason string
}

func (e *RecheckError) Error() string {
	return fmt.Sprintf("documents: index path recheck mismatch on %s: %s", e.Name, e.Reason)
}

// Recheck checks the candidates of one stream of a plan.
type Recheck struct {
	q *Query
	// exact are the filters of each scan of the stream; conj the disjunct it serves.
	exact [][]*Filter
	conj  []*Filter
	prev  []value.Value
}

// Recheck returns the check of stream s's candidates.
func (q *Query) Recheck(s *Stream) *Recheck {
	r := &Recheck{q: q, conj: s.Conjunction}
	for i := range s.Scans {
		r.exact = append(r.exact, s.Scans[i].Exact)
	}
	return r
}

// Check checks a candidate the stream read, the next in its order: a document that exists and
// matches every filter the stream's ranges hold. keep is false for one that does not match the
// rest of the stream's disjunct, which the runner would leave out; one that does must have every
// field the query orders by, and come after the one before it. A *RecheckError is a mismatch.
func (r *Recheck) Check(d *Doc) (keep bool, err error) {
	if d.UpdateTime.IsZero() {
		return false, r.mismatch(d, "an index entry of a document that does not exist")
	}
	for _, exact := range r.exact {
		for _, f := range exact {
			if !f.Matches(d) {
				return false, r.mismatch(d, fmt.Sprintf("the document does not match the %v filter on %s its index range holds",
					f.Op, f.Path))
			}
		}
	}
	for _, f := range r.conj {
		if !f.Matches(d) {
			return false, nil
		}
	}
	vals := make([]value.Value, len(r.q.Order))
	for i, o := range r.q.Order {
		v, ok := d.field(o.Path)
		if !ok {
			return false, r.mismatch(d, "the document has no "+o.Path.String()+", which its index holds")
		}
		vals[i] = v
	}
	if r.prev != nil {
		h := entries{order: r.q.Order}
		if h.compare(entry{vals: r.prev}, entry{vals: vals}) >= 0 {
			return false, r.mismatch(d, "the index returned it out of the query's order")
		}
	}
	r.prev = vals
	return true, nil
}

func (r *Recheck) mismatch(d *Doc, reason string) error {
	mismatches.Add(1)
	return &RecheckError{Name: d.Name, Reason: reason}
}
