package grpc

import (
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics counts the Document Database's RPCs: documents_rpcs_total by method and code, and
// documents_rpc_duration_seconds by method. The method label is the procedure of one of the
// three services served (procedures), or "unknown", so clients cannot create series.
type Metrics struct {
	rpcs     *prometheus.CounterVec
	duration *prometheus.HistogramVec
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
	}
}

// Collectors are the metrics to register.
func (m *Metrics) Collectors() []prometheus.Collector {
	return []prometheus.Collector{m.rpcs, m.duration}
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
