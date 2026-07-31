package sync

import (
	"log/slog"
	"reflect"
	"testing"

	"github.com/kentikethan/kentik_sync_agent/internal/core"
)

func TestFilterMostSpecific_Containment(t *testing.T) {
	candidates := []ipGroupCandidate{
		{ExternalID: "a", CIDR: "10.0.0.0/8", Tenant: "acme"},
		{ExternalID: "b", CIDR: "10.1.0.0/16", Tenant: "acme"},
		{ExternalID: "c", CIDR: "10.1.1.0/24", Tenant: "acme"},
		{ExternalID: "d", CIDR: "192.168.0.0/24", Tenant: "acme"},
	}
	kept := filterMostSpecific(candidates, slog.Default())

	if kept["a"] || kept["b"] {
		t.Fatalf("expected broader enclosing prefixes dropped, got kept=%v", kept)
	}
	if !kept["c"] {
		t.Fatalf("expected most specific nested prefix kept, got kept=%v", kept)
	}
	if !kept["d"] {
		t.Fatalf("expected disjoint prefix kept, got kept=%v", kept)
	}
}

func TestFilterMostSpecific_TieBreakDeterministic(t *testing.T) {
	candidates := []ipGroupCandidate{
		{ExternalID: "x", CIDR: "10.20.0.0/16", Tenant: "TenantB", VRF: "vrf-2"},
		{ExternalID: "y", CIDR: "10.20.0.0/16", Tenant: "TenantA", VRF: "vrf-1"},
	}
	kept := filterMostSpecific(candidates, slog.Default())
	if kept["x"] || !kept["y"] {
		t.Fatalf("expected alphabetically-first tenant (TenantA/y) to win deterministically, got kept=%v", kept)
	}

	// Order shouldn't matter.
	reversed := []ipGroupCandidate{candidates[1], candidates[0]}
	kept2 := filterMostSpecific(reversed, slog.Default())
	if kept2["x"] || !kept2["y"] {
		t.Fatalf("tie-break should be order-independent, got kept=%v", kept2)
	}
}

func TestFilterMostSpecific_UnparseableCIDRKeptOpen(t *testing.T) {
	candidates := []ipGroupCandidate{
		{ExternalID: "range", CIDR: "10.0.0.1-10.0.0.10", Tenant: "acme"},
		{ExternalID: "prefix", CIDR: "10.0.0.0/8", Tenant: "acme"},
	}
	kept := filterMostSpecific(candidates, slog.Default())
	if !kept["range"] {
		t.Fatalf("expected unparseable (hyphenated range) CIDR to be kept open, got kept=%v", kept)
	}
	if !kept["prefix"] {
		t.Fatalf("expected the sole comparable prefix to be kept, got kept=%v", kept)
	}
}

func TestIPGroupDestinations_ResolveTargets(t *testing.T) {
	d := &IPGroupDestinations{
		DefaultSourceDimensionID:      "default-src",
		DefaultDestinationDimensionID: "default-dst",
		Routes: []IPGroupRoute{
			{Tenant: "acme", SourceCustomDimensionID: "acme-src"},
			{Tenant: "acme", VRF: "vrf-blue", SourceCustomDimensionID: "acme-blue-src", DestinationCustomDimensionID: "acme-blue-dst"},
		},
	}

	// src-only direction targets just the resolved source dimension.
	if got := d.ResolveTargets("acme", "vrf-blue", core.IPGroupDirectionSrc); !reflect.DeepEqual(got, []string{"acme-blue-src"}) {
		t.Fatalf("expected exact tenant+vrf src match to win, got %v", got)
	}
	// dst-only direction, no route override for destination on this tenant/vrf: falls back to default.
	if got := d.ResolveTargets("acme", "vrf-green", core.IPGroupDirectionDst); !reflect.DeepEqual(got, []string{"default-dst"}) {
		t.Fatalf("expected tenant-only src route not to affect dst resolution, got %v", got)
	}
	// unmatched tenant uses both defaults.
	if got := d.ResolveTargets("other", "vrf-blue", core.IPGroupDirectionEither); !reflect.DeepEqual(got, []string{"default-src", "default-dst"}) {
		t.Fatalf("expected both default dimensions for unmatched tenant, got %v", got)
	}
	// "either" with distinct src/dst dimensions targets both.
	if got := d.ResolveTargets("acme", "vrf-blue", core.IPGroupDirectionEither); !reflect.DeepEqual(got, []string{"acme-blue-src", "acme-blue-dst"}) {
		t.Fatalf("expected either direction to target both resolved dimensions, got %v", got)
	}
	// "either" collapses to one target when src and dst resolve to the same dimension.
	same := &IPGroupDestinations{DefaultSourceDimensionID: "shared", DefaultDestinationDimensionID: "shared"}
	if got := same.ResolveTargets("acme", "", core.IPGroupDirectionEither); !reflect.DeepEqual(got, []string{"shared"}) {
		t.Fatalf("expected either direction to collapse to one target when src==dst, got %v", got)
	}
}
