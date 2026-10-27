package grpc

import (
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/arumadev/aruma/internal/services/documents/core"
)

// Metrics counts the Document Database's RPCs: documents_rpcs_total by method and code, and
// documents_rpc_duration_seconds by method. The method label is the procedure of one of the
// three services served (procedures), or "unknown", so clients cannot create series. And the
// streams' ends of their own (ADR-026 §3, §4, §5): documents_stream_overflows_total by the limit
// reached, and documents_stream_closes_total by why the server closed a Listen or Write stream.
type Metrics struct {
	rpcs      *prometheus.CounterVec
	duration  *prometheus.HistogramVec
	overflows *prometheus.CounterVec
	closes    *prometheus.CounterVec
}

// NewMetrics returns the metrics, to register with Collectors.
func NewMetrics() *Metrics {
	return &Metrics{
		rpcs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "documents_rpcs_total",
			Help: "Document Database gRPC calls, by method and status code.",
		}, []string{"method", "code"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "documents_rpc_duration_seconds",
			Help:    "Document Database gRPC call durations, streams included, by method.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method"}),
		overflows: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "documents_stream_overflows_total",
			Help: "Document Database stream limits reached, by the limit's setting.",
		}, []string{"limit"}),
		closes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "documents_stream_closes_total",
			Help: "Document Database Listen and Write streams the server closed, by reason: token_expired, token_revoked, instance_lost.",
		}, []string{"reason"}),
	}
}

// Collectors are the metrics to register.
func (m *Metrics) Collectors() []prometheus.Collector {
	return []prometheus.Collector{m.rpcs, m.duration, m.overflows, m.closes}
}

// overflow counts a stream limit reached; limit is the setting's name, a constant of ours.
func (m *Metrics) overflow(limit string) {
	if m != nil {
		m.overflows.WithLabelValues(limit).Inc()
	}
}

// closed counts a stream the server closed for reason, a constant of ours.
func (m *Metrics) closed(reason string) {
	if m != nil {
		m.closes.WithLabelValues(reason).Inc()
	}
}

// Overflows is the count of the limit's overflows, for tests.
func (m *Metrics) Overflows(limit string) float64 {
	return counterValue(m.overflows.WithLabelValues(limit))
}

// Closes is the count of the streams closed for reason, for tests.
func (m *Metrics) Closes(reason string) float64 {
	return counterValue(m.closes.WithLabelValues(reason))
}

func counterValue(c prometheus.Counter) float64 {
	var out dto.Metric
	if c.Write(&out) != nil {
		return 0
	}
	return out.GetCounter().GetValue()
}

func (m *Metrics) observe(procedure string, code connect.Code, d time.Duration) {
	if m == nil {
		return
	}
	method := "unknown"
	if procedures[procedure] {
		method = strings.TrimPrefix(procedure, "/")
	}
	m.rpcs.WithLabelValues(method, codeName(code)).Inc()
	m.duration.WithLabelValues(method).Observe(d.Seconds())
}

// TransactionCollectors are the transactions' counters (core.TxnStats), read from stats at each
// scrape: documents_txn_lock_waits_total, documents_txn_lock_timeouts_total,
// documents_txn_wounds_total, documents_txn_expired_total, documents_txn_cap_rejects_total,
// documents_txn_conflicts_total, and documents_lock_violation_total, which must stay 0: a
// pessimistic commit found a document it had read changed under its lock (ADR-024 §6.4).
func TransactionCollectors(stats func() core.TxnStats) []prometheus.Collector {
	counter := func(name, help string, v func(core.TxnStats) int64) prometheus.Collector {
		return prometheus.NewCounterFunc(prometheus.CounterOpts{Name: name, Help: help},
			func() float64 { return float64(v(stats())) })
	}
	return []prometheus.Collector{
		counter("documents_txn_lock_waits_total", "Document Database lock requests that queued.",
			func(s core.TxnStats) int64 { return s.Waits }),
		counter("documents_txn_lock_timeouts_total", "Document Database lock waits that gave up at the wait timeout.",
			func(s core.TxnStats) int64 { return s.Timeouts }),
		counter("documents_txn_wounds_total", "Document Database transactions wounded by an older one.",
			func(s core.TxnStats) int64 { return s.Wounds }),
		counter("documents_txn_expired_total", "Document Database transactions ended at their maximum lifetime.",
			func(s core.TxnStats) int64 { return s.Expired }),
		counter("documents_txn_cap_rejects_total", "Document Database BeginTransaction calls refused over the open transactions' bound.",
			func(s core.TxnStats) int64 { return s.CapRejects }),
		counter("documents_txn_conflicts_total", "Document Database optimistic commits aborted because what they read changed.",
			func(s core.TxnStats) int64 { return s.Conflicts }),
		counter("documents_lock_violation_total", "Document Database pessimistic commits that found a document they read changed under their lock; must stay 0.",
			func(s core.TxnStats) int64 { return s.LockViolations }),
	}
}
