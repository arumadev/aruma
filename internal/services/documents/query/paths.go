package query

import (
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
)

// Path is how a query was served (ADR-025 §1): the reference path, or one of the planner's index
// paths. The tests prove each path ran by its counter, so a planner that refuses everything
// cannot pass for one that works (PA §B4).
type Path uint8

const (
	PathReference Path = iota
	PathIndex
	PathIndexMerged
	PathIndexContains
	numPaths
)

var pathNames = [numPaths]string{"reference", "index", "index_merged", "index_contains"}

func (p Path) String() string { return pathNames[p] }

// Stats counts how one service served its queries since it started. Each service has its own,
// so tests that run at once each read only theirs; the app registers its service's (Collectors).
type Stats struct {
	// served counts the queries and aggregations served, by path.
	served [numPaths]atomic.Int64
	// entriesRead counts the index entries the index paths read.
	entriesRead atomic.Int64
	// unplanned counts the queries on fields the planner refused.
	unplanned atomic.Int64
	// mismatches counts the recheck mismatches (recheck.go).
	mismatches atomic.Int64
}

// Served counts one query served by p.
func (s *Stats) Served(p Path) { s.served[p].Add(1) }

// ServedCount is how many queries p served.
func (s *Stats) ServedCount(p Path) int64 { return s.served[p].Load() }

// IndexServed is how many queries the index paths served, all of them together.
func (s *Stats) IndexServed() int64 {
	return s.ServedCount(PathIndex) + s.ServedCount(PathIndexMerged) + s.ServedCount(PathIndexContains)
}

// EntriesRead counts n index entries an index path read.
func (s *Stats) EntriesRead(n int) { s.entriesRead.Add(int64(n)) }

// EntriesReadCount is how many index entries the index paths read.
func (s *Stats) EntriesReadCount() int64 { return s.entriesRead.Load() }

// Unplanned counts a query on fields the planner refused, which the reference path served: a
// performance opportunity (ADR-025 §1).
func (s *Stats) Unplanned() { s.unplanned.Add(1) }

// Mismatch counts a recheck mismatch: a query that failed with a *RecheckError.
func (s *Stats) Mismatch() { s.mismatches.Add(1) }

// MismatchCount is how many recheck mismatches there were.
func (s *Stats) MismatchCount() int64 { return s.mismatches.Load() }

// Collectors are s's counters to register: documents_query_path_total{path},
// documents_query_index_entries_read_total, documents_query_unplanned_total and
// documents_query_recheck_mismatch_total.
func (s *Stats) Collectors() []prometheus.Collector {
	out := make([]prometheus.Collector, 0, numPaths+3)
	for p := range numPaths {
		out = append(out, prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name:        "documents_query_path_total",
			Help:        "Document Database queries and aggregations served, by the path that served them.",
			ConstLabels: prometheus.Labels{"path": p.String()},
		}, func() float64 { return float64(s.served[p].Load()) }))
	}
	out = append(out, prometheus.NewCounterFunc(prometheus.CounterOpts{
		Name: "documents_query_index_entries_read_total",
		Help: "Document Database index entries the planner's index paths read.",
	}, func() float64 { return float64(s.entriesRead.Load()) }), prometheus.NewCounterFunc(prometheus.CounterOpts{
		Name: "documents_query_unplanned_total",
		Help: "Document Database queries on fields the planner refused, served on the reference path.",
	}, func() float64 { return float64(s.unplanned.Load()) }), prometheus.NewCounterFunc(prometheus.CounterOpts{
		Name: "documents_query_recheck_mismatch_total",
		Help: "Document Database index path candidates the reference matcher refused (each failed its query INTERNAL).",
	}, func() float64 { return float64(s.mismatches.Load()) }))
	return out
}
