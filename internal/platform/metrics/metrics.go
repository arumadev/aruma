// Package metrics exposes Prometheus metrics. They are served on a private address, never on
// the public entry point (ADR-008).
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/arumadev/aruma/internal/platform/httpx"
)

// Metrics is the server's registry and its shared collectors.
type Metrics struct {
	Registry *prometheus.Registry
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

// New creates a registry with Go runtime, process and HTTP collectors.
func New(version string) *Metrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	m := &Metrics{
		Registry: reg,
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "aruma_http_requests_total",
			Help: "HTTP requests by API area, method and status code.",
		}, []string{"area", "method", "code"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "aruma_http_request_duration_seconds",
			Help:    "HTTP request latency by API area.",
			Buckets: prometheus.DefBuckets,
		}, []string{"area"}),
	}
	build := prometheus.NewGauge(prometheus.GaugeOpts{
		Name:        "aruma_build_info",
		Help:        "Build information.",
		ConstLabels: prometheus.Labels{"version": version},
	})
	build.Set(1)
	reg.MustRegister(m.requests, m.duration, build)
	return m
}

// Instrument counts requests to one API area. Areas are a fixed, small set ("console",
// "mgmt", "oauth", "static", "health"), which keeps label cardinality bounded.
func (m *Metrics) Instrument(area string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &httpx.StatusRecorder{ResponseWriter: w, Status: http.StatusOK}
		next.ServeHTTP(rec, r)
		m.requests.WithLabelValues(area, Method(r.Method), strconv.Itoa(rec.Status)).Inc()
		m.duration.WithLabelValues(area).Observe(time.Since(start).Seconds())
	})
}

// Method maps an HTTP method to a fixed label set. Clients choose the method, so using it raw
// would let anyone create unbounded series.
func Method(m string) string {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodOptions:
		return m
	}
	return "OTHER"
}

// Handler serves the registry in the Prometheus text format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{Registry: m.Registry})
}
