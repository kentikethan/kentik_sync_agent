package observability

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	SyncRunDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "sync_run_duration_seconds",
		Help: "Duration of one sync pass for a (source, object_type) pair.",
	}, []string{"source", "object_type"})

	SyncObjectsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "sync_objects_total",
		Help: "Count of objects created/updated/deleted/skipped/failed per sync run.",
	}, []string{"source", "object_type", "action"})

	SyncFailuresTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "sync_failures_total",
		Help: "Count of sync failures, including fetch-level failures.",
	}, []string{"source", "object_type"})

	SyncLastSuccessTimestamp = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "sync_last_success_timestamp",
		Help: "Unix timestamp of the last sync run that completed without a fetch-level error.",
	}, []string{"source"})

	KentikAPIRequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "kentik_api_requests_total",
		Help: "Count of requests made to the Kentik API.",
	}, []string{"method", "status"})

	SourceAPIRequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "source_api_requests_total",
		Help: "Count of requests made to a source's API.",
	}, []string{"source", "status"})

	RateLimitDelaysTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "rate_limit_delays_total",
		Help: "Count of calls delayed by client-side rate limiting.",
	}, []string{"target"})
)

func init() {
	prometheus.MustRegister(
		SyncRunDuration,
		SyncObjectsTotal,
		SyncFailuresTotal,
		SyncLastSuccessTimestamp,
		KentikAPIRequestsTotal,
		SourceAPIRequestsTotal,
		RateLimitDelaysTotal,
	)
}

// ObjectCounts is the subset of sync.ObjectResult that metrics cares about.
// Defined locally (rather than importing internal/sync) to keep
// observability dependency-free of the sync engine.
type ObjectCounts struct {
	Created, Updated, Deleted, Skipped, Failed int
}

// RecordObjectResult records one object type's outcome for a sync run.
func RecordObjectResult(source, objectType string, c ObjectCounts, duration time.Duration) {
	SyncRunDuration.WithLabelValues(source, objectType).Observe(duration.Seconds())
	SyncObjectsTotal.WithLabelValues(source, objectType, "create").Add(float64(c.Created))
	SyncObjectsTotal.WithLabelValues(source, objectType, "update").Add(float64(c.Updated))
	SyncObjectsTotal.WithLabelValues(source, objectType, "delete").Add(float64(c.Deleted))
	SyncObjectsTotal.WithLabelValues(source, objectType, "skip").Add(float64(c.Skipped))
	if c.Failed > 0 {
		SyncFailuresTotal.WithLabelValues(source, objectType).Add(float64(c.Failed))
	}
}

// RecordSuccess marks a run as having completed without a fetch-level
// error at the given time.
func RecordSuccess(source string, at time.Time) {
	SyncLastSuccessTimestamp.WithLabelValues(source).Set(float64(at.Unix()))
}
