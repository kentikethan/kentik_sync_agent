package netbox

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/kentikethan/kentik_sync_agent/internal/core"
)

type netboxPrefix struct {
	ID          int         `json:"id"`
	Prefix      string      `json:"prefix"`
	Status      statusField `json:"status"`
	VRF         *nestedRef  `json:"vrf"`
	Role        *nestedRef  `json:"role"`
	Tenant      *nestedRef  `json:"tenant"`
	Scope       *nestedRef  `json:"scope"`
	Tags        []tagRef    `json:"tags"`
	LastUpdated string      `json:"last_updated"`
}

type netboxIPRange struct {
	ID           int         `json:"id"`
	StartAddress string      `json:"start_address"`
	EndAddress   string      `json:"end_address"`
	Status       statusField `json:"status"`
	VRF          *nestedRef  `json:"vrf"`
	Role         *nestedRef  `json:"role"`
	Tenant       *nestedRef  `json:"tenant"`
	Tags         []tagRef    `json:"tags"`
	LastUpdated  string      `json:"last_updated"`
}

// labelFor extracts the configured label field from a NetBox foreign-key
// set common to prefixes and IP ranges. "site" resolves to the generic
// `scope` field (which replaced the old `site` FK as of NetBox 4.2) when
// present; prefixes and IP ranges that don't carry a scope simply get an
// empty label for that option.
func labelFor(field string, vrf, role, tenant, scope *nestedRef) string {
	switch field {
	case "vrf":
		return vrf.label()
	case "role":
		return role.label()
	case "tenant":
		return tenant.label()
	case "site":
		return scope.label()
	default:
		return ""
	}
}

func (p netboxPrefix) toCore(labelField string, dir core.IPGroupDirection) core.IPGroup {
	return core.IPGroup{
		ExternalID: "prefix:" + strconv.Itoa(p.ID),
		CIDR:       p.Prefix,
		Label:      labelFor(labelField, p.VRF, p.Role, p.Tenant, p.Scope),
		Direction:  dir,
	}
}

// toCore renders an IP range as a hyphenated address range (e.g.
// "10.0.0.1-10.0.0.10"), which Kentik's Populator address field accepts in
// addition to CIDR notation.
func (r netboxIPRange) toCore(labelField string, dir core.IPGroupDirection) core.IPGroup {
	return core.IPGroup{
		ExternalID: "range:" + strconv.Itoa(r.ID),
		CIDR:       stripCIDR(r.StartAddress) + "-" + stripCIDR(r.EndAddress),
		Label:      labelFor(labelField, r.VRF, r.Role, r.Tenant, nil),
		Direction:  dir,
	}
}

// FetchIPGroups fetches NetBox IPAM prefixes and/or IP ranges (per the
// source's mapping.ip_groups.source config) and normalizes them into
// core.IPGroup, the source-agnostic input to Kentik's Populator API.
func (s *Source) FetchIPGroups(ctx context.Context, since string) (core.FetchResult[core.IPGroup], error) {
	fetchStart := time.Now()
	labelField := s.cfg.Mapping.IPGroups.LabelField
	dir := s.cfg.Mapping.IPGroups.Direction

	var items []core.IPGroup
	truncated := false

	if s.cfg.Mapping.IPGroups.Source == "prefixes" || s.cfg.Mapping.IPGroups.Source == "both" {
		raw, trunc, err := listAll[netboxPrefix](ctx, s, "/api/ipam/prefixes/", sinceQuery(since))
		if err != nil {
			return core.FetchResult[core.IPGroup]{}, fmt.Errorf("netbox: fetching prefixes: %w", err)
		}
		truncated = truncated || trunc
		for _, p := range raw {
			items = append(items, p.toCore(labelField, dir))
		}
	}

	if s.cfg.Mapping.IPGroups.Source == "ip_ranges" || s.cfg.Mapping.IPGroups.Source == "both" {
		raw, trunc, err := listAll[netboxIPRange](ctx, s, "/api/ipam/ip-ranges/", sinceQuery(since))
		if err != nil {
			return core.FetchResult[core.IPGroup]{}, fmt.Errorf("netbox: fetching IP ranges: %w", err)
		}
		truncated = truncated || trunc
		for _, r := range raw {
			items = append(items, r.toCore(labelField, dir))
		}
	}

	return core.FetchResult[core.IPGroup]{
		Items:     items,
		Cursor:    nowCursor(fetchStart),
		Truncated: truncated,
	}, nil
}
