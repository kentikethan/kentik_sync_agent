package netbox

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kentikethan/kentik_sync_agent/internal/core"
)

// newTestSource starts an httptest.Server serving handler and returns a
// Source configured to talk to it.
func newTestSource(t *testing.T, handler http.HandlerFunc) *Source {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return New(Config{
		URL:   srv.URL,
		Token: "test-token",
		Mapping: MappingConfig{
			IPGroups: IPGroupMapping{Source: "prefixes", LabelField: "vrf", Direction: core.IPGroupDirectionEither},
		},
	})
}

func TestFetchDevices_MapsFields(t *testing.T) {
	body := listResponse[netboxDevice]{
		Count: 1,
		Results: []netboxDevice{
			{
				ID:         42,
				Name:       "core-router-1",
				Site:       &nestedRef{ID: 7, Slug: "dc1"},
				DeviceType: &nestedRef{ID: 1, Slug: "mx480"},
				Role:       &nestedRef{ID: 2, Slug: "router"},
				Status:     statusField{Value: "active"},
				PrimaryIP4: &ipAddressRef{ID: 100, Address: "10.0.0.1/32"},
				Serial:     "SN123",
				Platform:   &nestedRef{ID: 3, Slug: "junos"},
				Tags:       []tagRef{{Slug: "prod"}},
				CustomFields: map[string]any{
					"asset_tag": "A-1",
				},
			},
		},
	}

	src := newTestSource(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/dcim/devices/" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Token test-token" {
			t.Fatalf("unexpected auth header %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	})

	result, err := src.FetchDevices(context.Background(), "")
	if err != nil {
		t.Fatalf("FetchDevices: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("expected 1 device, got %d", len(result.Items))
	}
	d := result.Items[0]
	if d.ExternalID != "42" || d.Name != "core-router-1" || d.SiteExternalID != "7" {
		t.Fatalf("unexpected device mapping: %+v", d)
	}
	if d.Status != core.DeviceStatusActive {
		t.Fatalf("expected active status, got %s", d.Status)
	}
	if d.PrimaryIPv4 != "10.0.0.1" {
		t.Fatalf("expected stripped CIDR, got %q", d.PrimaryIPv4)
	}
	if d.CustomFields["asset_tag"] != "A-1" {
		t.Fatalf("expected custom field passthrough, got %+v", d.CustomFields)
	}
	if result.Cursor == "" {
		t.Fatal("expected a non-empty cursor after a fetch")
	}
}

func TestFetchDevices_Pagination(t *testing.T) {
	calls := 0
	src := newTestSource(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			next := "http://" + r.Host + "/api/dcim/devices/?limit=500&offset=500"
			_ = json.NewEncoder(w).Encode(listResponse[netboxDevice]{
				Count:   2,
				Next:    &next,
				Results: []netboxDevice{{ID: 1, Name: "dev-1"}},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(listResponse[netboxDevice]{
			Count:   2,
			Results: []netboxDevice{{ID: 2, Name: "dev-2"}},
		})
	})

	result, err := src.FetchDevices(context.Background(), "")
	if err != nil {
		t.Fatalf("FetchDevices: %v", err)
	}
	if len(result.Items) != 2 {
		t.Fatalf("expected 2 devices across both pages, got %d", len(result.Items))
	}
	if calls != 2 {
		t.Fatalf("expected 2 requests (one per page), got %d", calls)
	}
}

func TestFetchSites_MapsFields(t *testing.T) {
	lat, lon := 37.7749, -122.4194
	src := newTestSource(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(listResponse[netboxSite]{
			Count: 1,
			Results: []netboxSite{
				{
					ID:          7,
					Name:        "DC1",
					Status:      statusField{Value: "active"},
					Latitude:    &lat,
					Longitude:   &lon,
					PhysicalAdr: "123 Main St",
					Region:      &nestedRef{Slug: "us-west"},
				},
			},
		})
	})

	result, err := src.FetchSites(context.Background(), "")
	if err != nil {
		t.Fatalf("FetchSites: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("expected 1 site, got %d", len(result.Items))
	}
	s := result.Items[0]
	if s.ExternalID != "7" || s.Name != "DC1" || s.Status != core.SiteStatusActive {
		t.Fatalf("unexpected site mapping: %+v", s)
	}
	if s.Latitude == nil || *s.Latitude != lat {
		t.Fatalf("expected latitude %v, got %+v", lat, s.Latitude)
	}
}

func TestFetchIPGroups_PrefixesWithLabelField(t *testing.T) {
	src := newTestSource(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ipam/prefixes/" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(listResponse[netboxPrefix]{
			Count: 1,
			Results: []netboxPrefix{
				{ID: 55, Prefix: "10.1.0.0/24", VRF: &nestedRef{Slug: "vrf-blue"}},
			},
		})
	})

	result, err := src.FetchIPGroups(context.Background(), "")
	if err != nil {
		t.Fatalf("FetchIPGroups: %v", err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("expected 1 IP group, got %d", len(result.Items))
	}
	g := result.Items[0]
	if g.CIDR != "10.1.0.0/24" || g.Label != "vrf-blue" || g.Direction != core.IPGroupDirectionEither {
		t.Fatalf("unexpected IP group mapping: %+v", g)
	}
}

func TestFetchDevices_RateLimited(t *testing.T) {
	src := newTestSource(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	if _, err := src.FetchDevices(context.Background(), ""); err == nil {
		t.Fatal("expected an error on 429 response")
	}
}

func TestFetchDeviceLabels_MapsTenantAndRole(t *testing.T) {
	src := newTestSource(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/dcim/devices/" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(listResponse[netboxDevice]{
			Count: 3,
			Results: []netboxDevice{
				{ID: 1, Tenant: &nestedRef{Slug: "acme"}, Role: &nestedRef{Slug: "router"}},
				{ID: 2, Tenant: &nestedRef{Slug: "acme"}},
				{ID: 3},
			},
		})
	})

	result, err := src.FetchDeviceLabels(context.Background(), "")
	if err != nil {
		t.Fatalf("FetchDeviceLabels: %v", err)
	}
	if len(result.Items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(result.Items))
	}

	byID := map[string][]string{}
	for _, it := range result.Items {
		byID[it.ExternalID] = it.Labels
	}

	want1 := []string{"Netbox:Device:Tenant:acme", "Netbox:Device:Role:router"}
	if got := byID["1"]; len(got) != 2 || got[0] != want1[0] || got[1] != want1[1] {
		t.Fatalf("unexpected labels for device 1: %+v", got)
	}
	if got := byID["2"]; len(got) != 1 || got[0] != "Netbox:Device:Tenant:acme" {
		t.Fatalf("expected tenant-only label for device 2, got %+v", got)
	}
	if got := byID["3"]; len(got) != 0 {
		t.Fatalf("expected no labels for a device with no tenant/role, got %+v", got)
	}
}

func TestSinceQuery(t *testing.T) {
	q := sinceQuery("")
	if len(q) != 0 {
		t.Fatalf("expected empty query for full fetch, got %v", q)
	}
	q = sinceQuery("2026-01-01T00:00:00Z")
	if q.Get("last_updated__gte") != "2026-01-01T00:00:00Z" {
		t.Fatalf("expected last_updated__gte filter, got %v", q)
	}
}
