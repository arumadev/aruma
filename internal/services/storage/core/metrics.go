package core

import "github.com/prometheus/client_golang/prometheus"

// Metrics are Storage's collectors (ADR-022 §18), declared with the contract so no task invents
// its own names or labels. Requests by surface, method and status come from the entry point
// (areas storage and gcs). Each collector names the task that reports to it; until then it stays
// at zero. app registers them on the server's registry.
type Metrics struct {
	// Bytes counts object bytes by direction (in, out) and surface (firebase, gcs, console):
	// S3-B, S3-C, S4-B, S4-C, S5-B.
	Bytes *prometheus.CounterVec
	// Sessions and StagingBytes are the live upload sessions and their staged bytes: S2-D.
	Sessions     prometheus.Gauge
	StagingBytes prometheus.Gauge
	// Rejections counts writes refused for capacity, by reason (quota, anonymous_quota, disk,
	// too_many_uploads, start_rate): S2-D, and start_rate the surfaces.
	Rejections *prometheus.CounterVec
	// RulesDecisions counts rules outcomes (allow, deny, overloaded), and RulesSeconds their
	// time: S2-E.
	RulesDecisions *prometheus.CounterVec
	RulesSeconds   prometheus.Histogram
	// GCQueue and GCOldestSeconds are blob_gc's length and the age of its oldest row: S2-D.
	GCQueue         prometheus.Gauge
	GCOldestSeconds prometheus.Gauge
	// SweepDeleted counts the orphans Boot's candidates yielded; SweepReported what the periodic
	// sweep found, by kind (file_without_row, row_without_file): S2-D.
	SweepDeleted  prometheus.Counter
	SweepReported *prometheus.CounterVec
	// DecompressionsCut counts gzip replies cut at object_bytes (media.Serve's error): S3-C,
	// S4-C.
	DecompressionsCut prometheus.Counter
	// DeleteFailures counts the synchronous deletes of a new file that failed: S2-D.
	DeleteFailures prometheus.Counter
	// AmbiguousCommits counts commits whose outcome was unknown, by how they were settled
	// (committed, aborted, unknown): S2-D.
	AmbiguousCommits *prometheus.CounterVec
	// LockWaitSeconds is how long Boot waited for the data directory's lock: S2-D.
	LockWaitSeconds prometheus.Gauge
	// QuotaReconciledBytes counts the bytes Boot's reconciliation corrected: S2-D.
	QuotaReconciledBytes prometheus.Counter
}

// NewMetrics makes Storage's collectors, unregistered.
func NewMetrics() *Metrics {
	return &Metrics{
		Bytes: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "aruma_storage_bytes_total",
			Help: "Object bytes by direction and surface."}, []string{"direction", "surface"}),
		Sessions: prometheus.NewGauge(prometheus.GaugeOpts{Name: "aruma_storage_upload_sessions",
			Help: "Live upload sessions."}),
		StagingBytes: prometheus.NewGauge(prometheus.GaugeOpts{Name: "aruma_storage_staging_bytes",
			Help: "Bytes staged by live upload sessions."}),
		Rejections: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "aruma_storage_rejections_total",
			Help: "Writes refused for capacity, by reason."}, []string{"reason"}),
		RulesDecisions: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "aruma_storage_rules_decisions_total",
			Help: "Storage rules decisions by result."}, []string{"result"}),
		RulesSeconds: prometheus.NewHistogram(prometheus.HistogramOpts{Name: "aruma_storage_rules_seconds",
			Help: "Time of one Storage rules decision.", Buckets: prometheus.ExponentialBuckets(0.0001, 4, 8)}),
		GCQueue: prometheus.NewGauge(prometheus.GaugeOpts{Name: "aruma_storage_gc_queue",
			Help: "Files waiting in blob_gc."}),
		GCOldestSeconds: prometheus.NewGauge(prometheus.GaugeOpts{Name: "aruma_storage_gc_oldest_seconds",
			Help: "Age of the oldest file waiting in blob_gc."}),
		SweepDeleted: prometheus.NewCounter(prometheus.CounterOpts{Name: "aruma_storage_sweep_deleted_total",
			Help: "Orphan files deleted after boot."}),
		SweepReported: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "aruma_storage_sweep_reported_total",
			Help: "Inconsistencies the periodic sweep reported, by kind."}, []string{"kind"}),
		DecompressionsCut: prometheus.NewCounter(prometheus.CounterOpts{Name: "aruma_storage_decompressions_cut_total",
			Help: "Decompressed downloads cut at object_bytes."}),
		DeleteFailures: prometheus.NewCounter(prometheus.CounterOpts{Name: "aruma_storage_delete_failures_total",
			Help: "Synchronous deletes of a new file that failed."}),
		AmbiguousCommits: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "aruma_storage_ambiguous_commits_total",
			Help: "Commits with an unknown outcome, by how they were settled."}, []string{"outcome"}),
		LockWaitSeconds: prometheus.NewGauge(prometheus.GaugeOpts{Name: "aruma_storage_lock_wait_seconds",
			Help: "How long boot waited for the data directory's lock."}),
		QuotaReconciledBytes: prometheus.NewCounter(prometheus.CounterOpts{Name: "aruma_storage_quota_reconciled_bytes_total",
			Help: "Bytes the boot reconciliation corrected in the quota accounts."}),
	}
}

// Collectors returns every collector, for registration.
func (m *Metrics) Collectors() []prometheus.Collector {
	return []prometheus.Collector{m.Bytes, m.Sessions, m.StagingBytes, m.Rejections, m.RulesDecisions, m.RulesSeconds,
		m.GCQueue, m.GCOldestSeconds, m.SweepDeleted, m.SweepReported, m.DecompressionsCut, m.DeleteFailures,
		m.AmbiguousCommits, m.LockWaitSeconds, m.QuotaReconciledBytes}
}
