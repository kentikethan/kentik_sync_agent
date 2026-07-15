package observability

import (
	"context"
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// ReadinessCheck reports whether the agent is ready to serve (config
// loaded, state store reachable, etc.). Returning an error marks /readyz
// unhealthy.
type ReadinessCheck func(ctx context.Context) error

// NewHealthMux builds the HTTP handler serving /healthz (liveness — always
// 200 once the process is up) and /readyz (readiness — runs check).
func NewHealthMux(check ReadinessCheck) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := check(r.Context()); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(err.Error()))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	return mux
}

// NewMetricsMux builds the HTTP handler serving /metrics for Prometheus
// scraping.
func NewMetricsMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	return mux
}
