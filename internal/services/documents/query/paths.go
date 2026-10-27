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

// served counts the queries and aggregations served, by path, since the process started.
var served [numPaths]atomic.Int64

// Served counts one query served by p.
func Served(p Path) { served[p].Add(1) }

// ServedCount is how many queries p served since the process started.
func ServedCount(p Path) int64 { return served[p].Load() }

// entriesRead counts the index entries the index paths read since the process started.
var entriesRead atomic.Int64

// EntriesRead counts n index entries an index path read.
func EntriesRead(n int) { entriesRead.Add(int64(n)) }

// EntriesReadCount is how many index entries the index paths read since the process started.
func EntriesReadCount() int64 { return entriesRead.Load() }

// unplanned counts the queries on fields the planner refused since the process started.
var unplanned atomic.Int64

// Unplanned counts a query on fields the planner refused, which the reference path served: a
// performance opportunity (ADR-025 §1).
func Unplanned() { unplanned.Add(1) }

// Collectors are the counters to register: documents_query_path_total{path},
// documents_query_index_entries_read_total, documents_query_unplanned_total and
// documents_query_recheck_mismatch_total (recheck.go).
func Collectors() []prometheus.Collector {
	out := make([]prometheus.Collector, 0, numPaths+3)
	for p := range numPaths {
		out = append(out, prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name:        "documents_query_path_total",
			Help:        "Document Database queries and aggregations served, by the path that served them.",
			ConstLabels: prometheus.Labels{"path": p.String()},
		}, func() float64 { return float64(served[p].Load()) }))
	}
	out = append(out, prometheus.NewCounterFunc(prometheus.CounterOpts{
		Name: "documents_query_index_entries_read_total",
		Help: "Document Database index entries the planner's index paths read.",
	}, func() float64 { return float64(entriesRead.Load()) }), prometheus.NewCounterFunc(prometheus.CounterOpts{
		Name: "documents_query_unplanned_total",
		Help: "Document Database queries on fields the planner refused, served on the reference path.",
	}, func() float64 { return float64(unplanned.Load()) }))
	return append(out, mismatchCollector())
}
