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

// Collectors are the path counters to register: documents_query_path_total{path}.
func Collectors() []prometheus.Collector {
	out := make([]prometheus.Collector, 0, numPaths)
	for p := range numPaths {
		out = append(out, prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name:        "documents_query_path_total",
			Help:        "Document Database queries and aggregations served, by the path that served them.",
			ConstLabels: prometheus.Labels{"path": p.String()},
		}, func() float64 { return float64(served[p].Load()) }))
	}
	return out
}
