package kentik

import (
	"testing"

	"github.com/kentikethan/kentik_sync_agent/internal/core"
)

func TestBuildDeviceConcise(t *testing.T) {
	cfg := Config{DefaultPlanID: 999, DefaultDeviceSubtype: "host-nprobe-dns-www"}
	d := core.Device{
		ExternalID:  "1",
		Name:        "core-router-1",
		PrimaryIPv4: "10.0.0.1",
		PrimaryIPv6: "fe80::1",
		Role:        "router",
		DeviceType:  "mx480",
	}

	dc := buildDeviceConcise(d, 55, "", cfg)
	if dc.GetDeviceName() != "core-router-1" {
		t.Fatalf("unexpected device name: %q", dc.GetDeviceName())
	}
	if dc.GetPlanId() != 999 || dc.GetDeviceSubtype() != "host-nprobe-dns-www" {
		t.Fatalf("expected config defaults applied, got plan=%d subtype=%q", dc.GetPlanId(), dc.GetDeviceSubtype())
	}
	if dc.GetSiteId() != 55 {
		t.Fatalf("expected resolved site id 55, got %d", dc.GetSiteId())
	}
	if len(dc.GetSendingIps()) != 2 || dc.GetSendingIps()[0] != "10.0.0.1" || dc.GetSendingIps()[1] != "fe80::1" {
		t.Fatalf("expected both v4 and v6 in sending_ips, got %v", dc.GetSendingIps())
	}
	if dc.GetId() != "" {
		t.Fatalf("expected empty id for a create, got %q", dc.GetId())
	}

	updated := buildDeviceConcise(d, 55, "kentik-123", cfg)
	if updated.GetId() != "kentik-123" {
		t.Fatalf("expected id to be set for an update, got %q", updated.GetId())
	}
}

func TestBuildSite(t *testing.T) {
	lat, lon := 1.5, -2.5
	s := core.Site{ExternalID: "7", Name: "DC1", Latitude: &lat, Longitude: &lon, Address: "1 Main St", Region: "us-west"}

	created := buildSite(s, "")
	if created.GetTitle() != "DC1" || created.GetLat() != lat || created.GetLon() != lon {
		t.Fatalf("unexpected site translation: %+v", created)
	}
	if created.GetPostalAddress().GetAddress() != "1 Main St" || created.GetPostalAddress().GetRegion() != "us-west" {
		t.Fatalf("expected postal address populated, got %+v", created.GetPostalAddress())
	}

	updated := buildSite(s, "kentik-site-1")
	if updated.GetId() != "kentik-site-1" {
		t.Fatalf("expected id set for update, got %q", updated.GetId())
	}
}

func TestBuildSite_NoAddressOrRegionLeavesPostalAddressNil(t *testing.T) {
	s := core.Site{ExternalID: "1", Name: "Branch"}
	built := buildSite(s, "")
	if built.GetPostalAddress() != nil {
		t.Fatalf("expected nil postal address when no address/region given, got %+v", built.GetPostalAddress())
	}
}

func TestBuildPopulator(t *testing.T) {
	g := core.IPGroup{ExternalID: "prefix:1", CIDR: "10.1.0.0/24", Label: "vrf-blue", Direction: core.IPGroupDirectionSrc}
	p := buildPopulator(g, "")
	if p.GetValue() != "vrf-blue" || p.GetDirection() != "src" || len(p.GetAddr()) != 1 || p.GetAddr()[0] != "10.1.0.0/24" {
		t.Fatalf("unexpected populator translation: %+v", p)
	}

	defaulted := buildPopulator(core.IPGroup{CIDR: "10.2.0.0/24"}, "")
	if defaulted.GetDirection() != "either" {
		t.Fatalf("expected default direction 'either', got %q", defaulted.GetDirection())
	}
}

func TestGRPCTarget(t *testing.T) {
	cases := map[string]string{
		"":                             defaultHost,
		"grpc.api.kentik.com:443":      "grpc.api.kentik.com:443",
		"https://grpc.api.kentik.com":  "grpc.api.kentik.com:443",
		"http://localhost:50051":       "localhost:50051",
		"grpc.api.kentik.com":          "grpc.api.kentik.com:443",
		"https://grpc.api.kentik.com/": "grpc.api.kentik.com:443",
	}
	for in, want := range cases {
		if got := grpcTarget(in); got != want {
			t.Errorf("grpcTarget(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestChunkStrings(t *testing.T) {
	items := []string{"a", "b", "c", "d", "e"}
	chunks := chunkStrings(items, 2)
	if len(chunks) != 3 || len(chunks[0]) != 2 || len(chunks[2]) != 1 {
		t.Fatalf("unexpected chunking: %+v", chunks)
	}
}
