package observability

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kentikethan/kentik_sync_agent/internal/config"
	syncpkg "github.com/kentikethan/kentik_sync_agent/internal/sync"
)

// SyncMetrics captures one completed sync job's operational data for
// reporting to Kentik NMS as an InfluxDB line-protocol point.
type SyncMetrics struct {
	AgentName  string
	SourceName string
	SourceType string
	Endpoint   string
	Duration   time.Duration
	Success    bool

	FetchErrors  int
	Sites        syncpkg.ObjectResult
	Devices      syncpkg.ObjectResult
	IPGroups     syncpkg.ObjectResult
	DeviceLabels syncpkg.ObjectResult
}

// NewSyncMetrics maps a completed job/result into SyncMetrics. The single
// place this mapping happens, so it isn't duplicated across the --once and
// scheduled run paths that both report metrics.
func NewSyncMetrics(agentName string, job syncpkg.Job, result syncpkg.Result, duration time.Duration) SyncMetrics {
	return SyncMetrics{
		AgentName:  agentName,
		SourceName: job.SourceName,
		SourceType: job.Source.Name(),
		Endpoint:   job.Source.Endpoint(),
		Duration:   duration,
		Success:    !result.HasFailures(),

		FetchErrors:  len(result.FetchErrors),
		Sites:        result.Sites,
		Devices:      result.Devices,
		IPGroups:     result.IPGroups,
		DeviceLabels: result.DeviceLabels,
	}
}

// lineProtocol formats m as one InfluxDB line-protocol point, targeting
// measurement /kentik/sync-agent/<source type> (e.g.
// /kentik/sync-agent/netbox), per
// https://www.kentik.com/blog/using-telegraf-to-feed-api-json-data-into-kentik-nms/
func (m SyncMetrics) lineProtocol() string {
	measurement := "/kentik/sync-agent/" + escapeLP(m.SourceType)

	tags := strings.Join([]string{
		"agent_name=" + escapeLP(m.AgentName),
		"source_name=" + escapeLP(m.SourceName),
		"endpoint=" + escapeLP(m.Endpoint),
	}, ",")

	fields := []string{
		"duration_ms=" + strconv.FormatInt(m.Duration.Milliseconds(), 10) + "i",
		"success=" + boolField(m.Success),
		"fetch_errors=" + strconv.Itoa(m.FetchErrors) + "i",
	}
	fields = append(fields, objectFields("sites", m.Sites)...)
	fields = append(fields, objectFields("devices", m.Devices)...)
	fields = append(fields, objectFields("ip_groups", m.IPGroups)...)
	fields = append(fields, objectFields("device_labels", m.DeviceLabels)...)

	return fmt.Sprintf("%s,%s %s %d", measurement, tags, strings.Join(fields, ","), time.Now().UnixNano())
}

func objectFields(prefix string, r syncpkg.ObjectResult) []string {
	return []string{
		fmt.Sprintf("%s_created=%di", prefix, r.Created),
		fmt.Sprintf("%s_updated=%di", prefix, r.Updated),
		fmt.Sprintf("%s_deleted=%di", prefix, r.Deleted),
		fmt.Sprintf("%s_skipped=%di", prefix, r.Skipped),
		fmt.Sprintf("%s_failed=%di", prefix, r.Failed),
	}
}

func boolField(b bool) string {
	if b {
		return "1i"
	}
	return "0i"
}

// lpEscaper backslash-escapes the three characters InfluxDB line protocol
// treats specially in measurement names, tag keys, and tag values: comma,
// equals sign, and space (per the blog's own example: `description=Google\
// Wifi`).
var lpEscaper = strings.NewReplacer(",", `\,`, "=", `\=`, " ", `\ `)

func escapeLP(s string) string { return lpEscaper.Replace(s) }

// MetricsReporter pushes SyncMetrics to Kentik NMS. Fire-and-forget: never
// returns an error, and never retries — a metrics-push failure logs a
// warning and must never affect the sync job's own outcome or exit code.
type MetricsReporter struct {
	cfg      config.KentikMetricsConfig
	email    string
	apiToken string
	http     *http.Client
	log      *slog.Logger
}

func NewMetricsReporter(cfg config.KentikMetricsConfig, email, apiToken string, log *slog.Logger) *MetricsReporter {
	return &MetricsReporter{
		cfg:      cfg,
		email:    email,
		apiToken: apiToken,
		http:     &http.Client{Timeout: 10 * time.Second},
		log:      log,
	}
}

// Report sends one line-protocol point for m. No-op if r is nil (so
// callers that don't wire up a reporter, e.g. tests, don't need their own
// nil check) or metrics are disabled. Bounded by the reporter's own 10s
// HTTP client timeout, independent of ctx's deadline (or lack of one) — a
// slow/unreachable metrics endpoint must never hold up or fail the
// caller's sync job.
func (r *MetricsReporter) Report(ctx context.Context, m SyncMetrics) {
	if r == nil || r.cfg.Disabled {
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.cfg.URL, strings.NewReader(m.lineProtocol()))
	if err != nil {
		r.log.Warn("kentik metrics: building request failed", "error", err)
		return
	}
	req.Header.Set("X-CH-Auth-Email", r.email)
	req.Header.Set("X-CH-Auth-API-Token", r.apiToken)
	req.Header.Set("Content-Type", "application/influx")

	resp, err := r.http.Do(req)
	if err != nil {
		r.log.Warn("kentik metrics: push failed", "error", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		r.log.Warn("kentik metrics: push rejected", "status", resp.Status)
	}
}
