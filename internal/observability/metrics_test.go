package observability

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kentikethan/kentik_sync_agent/internal/config"
	"github.com/kentikethan/kentik_sync_agent/internal/core"
	"github.com/kentikethan/kentik_sync_agent/internal/source"
	syncpkg "github.com/kentikethan/kentik_sync_agent/internal/sync"
)

// fakeSource implements source.Source with just enough behavior for
// NewSyncMetrics — Name()/Endpoint() are the only methods it exercises.
type fakeSource struct{ endpoint string }

func (f fakeSource) Name() string                    { return "netbox" }
func (f fakeSource) Endpoint() string                { return f.endpoint }
func (f fakeSource) Capabilities() []core.ObjectType { return nil }
func (f fakeSource) SupportsIncremental() bool       { return false }
func (f fakeSource) FetchDevices(context.Context, string) (core.FetchResult[core.Device], error) {
	return core.FetchResult[core.Device]{}, nil
}
func (f fakeSource) FetchSites(context.Context, string) (core.FetchResult[core.Site], error) {
	return core.FetchResult[core.Site]{}, nil
}
func (f fakeSource) FetchIPGroups(context.Context, string) (core.FetchResult[core.IPGroup], error) {
	return core.FetchResult[core.IPGroup]{}, nil
}
func (f fakeSource) FetchDeviceLabels(context.Context, string) (core.FetchResult[core.DeviceLabels], error) {
	return core.FetchResult[core.DeviceLabels]{}, nil
}
func (f fakeSource) HealthCheck(context.Context) error { return nil }

var _ source.Source = fakeSource{}

func TestSyncMetrics_LineProtocol(t *testing.T) {
	job := syncpkg.Job{
		SourceName: "netbox primary", // space, to exercise tag-value escaping
		Source:     fakeSource{endpoint: "https://netbox.example.com"},
	}
	result := syncpkg.Result{
		Sites:   syncpkg.ObjectResult{Created: 1, Updated: 2},
		Devices: syncpkg.ObjectResult{Created: 3, Failed: 1, Errors: []error{errBoom{}}},
	}

	m := NewSyncMetrics("agent-1", job, result, 250*time.Millisecond)
	line := m.lineProtocol()

	if !strings.HasPrefix(line, "/kentik/sync-agent/netbox,") {
		t.Fatalf("expected measurement prefix, got: %s", line)
	}
	if !strings.Contains(line, `source_name=netbox\ primary`) {
		t.Fatalf("expected escaped space in source_name tag, got: %s", line)
	}
	if !strings.Contains(line, "endpoint=https://netbox.example.com") {
		t.Fatalf("expected endpoint tag, got: %s", line)
	}
	if !strings.Contains(line, "duration_ms=250i") {
		t.Fatalf("expected duration_ms field, got: %s", line)
	}
	if !strings.Contains(line, "sites_created=1i") || !strings.Contains(line, "sites_updated=2i") {
		t.Fatalf("expected sites counts, got: %s", line)
	}
	if !strings.Contains(line, "devices_created=3i") || !strings.Contains(line, "devices_failed=1i") {
		t.Fatalf("expected devices counts, got: %s", line)
	}
	if !strings.Contains(line, "success=0i") {
		t.Fatalf("expected success=0i since devices had a failure, got: %s", line)
	}

	// Line protocol is measurement+tags, then a space, then fields, then a
	// space, then the timestamp — exactly two *unescaped* spaces total
	// (the tag-value space above is backslash-escaped and doesn't count).
	if n := strings.Count(line, " ") - strings.Count(line, `\ `); n != 2 {
		t.Fatalf("expected exactly 2 unescaped spaces (tags|fields|timestamp separators), got %d in: %q", n, line)
	}
}

type errBoom struct{}

func (errBoom) Error() string { return "boom" }

func TestMetricsReporter_Report(t *testing.T) {
	var gotHeaders http.Header
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	reporter := NewMetricsReporter(config.KentikMetricsConfig{URL: srv.URL}, "user@example.com", "token-123", slog.Default())
	job := syncpkg.Job{SourceName: "netbox-primary", Source: fakeSource{endpoint: "https://netbox.example.com"}}
	m := NewSyncMetrics("agent-1", job, syncpkg.Result{}, time.Second)

	reporter.Report(context.Background(), m)

	if gotHeaders.Get("X-CH-Auth-Email") != "user@example.com" {
		t.Fatalf("expected email header, got %q", gotHeaders.Get("X-CH-Auth-Email"))
	}
	if gotHeaders.Get("X-CH-Auth-API-Token") != "token-123" {
		t.Fatalf("expected token header, got %q", gotHeaders.Get("X-CH-Auth-API-Token"))
	}
	if gotHeaders.Get("Content-Type") != "application/influx" {
		t.Fatalf("expected application/influx content-type, got %q", gotHeaders.Get("Content-Type"))
	}
	if !strings.HasPrefix(gotBody, "/kentik/sync-agent/netbox,") {
		t.Fatalf("expected line-protocol body, got %q", gotBody)
	}
}

func TestMetricsReporter_Report_Disabled(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	reporter := NewMetricsReporter(config.KentikMetricsConfig{URL: srv.URL, Disabled: true}, "user@example.com", "token-123", slog.Default())
	job := syncpkg.Job{SourceName: "netbox-primary", Source: fakeSource{endpoint: "https://netbox.example.com"}}
	m := NewSyncMetrics("agent-1", job, syncpkg.Result{}, time.Second)

	reporter.Report(context.Background(), m)

	if called {
		t.Fatal("expected no HTTP call when metrics are disabled")
	}
}
