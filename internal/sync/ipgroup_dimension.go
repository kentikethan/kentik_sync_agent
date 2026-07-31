package sync

import (
	"log/slog"
	"net/netip"

	"github.com/kentikethan/kentik_sync_agent/internal/core"
	"github.com/kentikethan/kentik_sync_agent/internal/destination/kentik"
)

// IPGroupRoute routes one tenant (optionally scoped to one VRF) to Custom
// Dimension(s) other than IPGroupDestinations' defaults. An empty ID falls
// back to the corresponding default dimension for that side.
type IPGroupRoute struct {
	Tenant string
	// VRF, if empty, matches Tenant across all VRFs.
	VRF                          string
	SourceCustomDimensionID      string
	DestinationCustomDimensionID string
}

// IPGroupDestinations resolves which Kentik Custom Dimension(s) an IP
// group's tenant/VRF/direction targets, and holds one PopulatorApplier per
// distinct dimension referenced by config. A single dimension's populators
// only match one traffic side, so an "either"-direction IP group targets
// both the source and destination dimensions (as two separate populators);
// "src"/"dst" target only the matching one. Everything not matched by
// Routes uses the Default*DimensionID fields.
type IPGroupDestinations struct {
	DefaultSourceDimensionID      string
	DefaultDestinationDimensionID string
	Routes                        []IPGroupRoute
	Appliers                      map[string]*kentik.PopulatorApplier
}

func (d *IPGroupDestinations) resolveSide(tenant, vrf string, routeID func(IPGroupRoute) string, defaultID string) string {
	for _, r := range d.Routes {
		if r.Tenant == tenant && r.VRF != "" && r.VRF == vrf {
			if id := routeID(r); id != "" {
				return id
			}
			return defaultID
		}
	}
	for _, r := range d.Routes {
		if r.Tenant == tenant && r.VRF == "" {
			if id := routeID(r); id != "" {
				return id
			}
			return defaultID
		}
	}
	return defaultID
}

// ResolveTargets returns every dimension ID an IP group with this
// tenant/vrf/direction should be written to as a populator — one ID for
// "src" or "dst", both (deduped, in case a route collapses them to the
// same dimension) for "either".
func (d *IPGroupDestinations) ResolveTargets(tenant, vrf string, dir core.IPGroupDirection) []string {
	srcID := d.resolveSide(tenant, vrf, func(r IPGroupRoute) string { return r.SourceCustomDimensionID }, d.DefaultSourceDimensionID)
	dstID := d.resolveSide(tenant, vrf, func(r IPGroupRoute) string { return r.DestinationCustomDimensionID }, d.DefaultDestinationDimensionID)

	switch dir {
	case core.IPGroupDirectionSrc:
		return []string{srcID}
	case core.IPGroupDirectionDst:
		return []string{dstID}
	default: // "either" or unset
		if srcID == dstID {
			return []string{srcID}
		}
		return []string{srcID, dstID}
	}
}

// Applier returns the PopulatorApplier for dimensionID. Callers only ever
// pass IDs produced by Resolve (or a stored CustomDimensionID that was
// itself produced by a prior Resolve call against the same config), so a
// miss here would indicate a config/wiring bug rather than a runtime
// condition to handle gracefully.
func (d *IPGroupDestinations) Applier(dimensionID string) *kentik.PopulatorApplier {
	return d.Appliers[dimensionID]
}

// ipGroupCandidate is one IP group under containment/collision
// consideration within a single Kentik Custom Dimension.
type ipGroupCandidate struct {
	ExternalID string
	CIDR       string
	Tenant     string
	VRF        string
}

// filterMostSpecific returns the set of ExternalIDs (from candidates) that
// should have a live Kentik populator: for any two candidates whose CIDRs
// overlap, only the more specific (longer-prefix) one survives; broader
// enclosing ranges are dropped (logged at Info, since this is routine
// NetBox-hierarchy behavior). For a genuine tie — identical/overlapping
// CIDRs with no containment relationship (e.g. the same range under two
// tenants) — a deterministic winner is picked (alphabetically-first
// Tenant, then VRF, then CIDR, then ExternalID as a final tiebreaker) and
// the loser is logged at Warn, per the confirmed tie-break policy.
//
// Candidates whose CIDR doesn't parse as a prefix (e.g. a NetBox IP range
// rendered as a hyphenated "start-end" address pair, which Kentik's
// Populator API accepts but which isn't expressible as a netip.Prefix) are
// kept unconditionally — containment can't be reasoned about for them, so
// this fails open rather than dropping data it can't analyze.
func filterMostSpecific(candidates []ipGroupCandidate, log *slog.Logger) map[string]bool {
	type parsedCandidate struct {
		candidate ipGroupCandidate
		prefix    netip.Prefix
	}

	kept := make(map[string]bool, len(candidates))
	var comparable []parsedCandidate
	for _, c := range candidates {
		prefix, err := netip.ParsePrefix(c.CIDR)
		if err != nil {
			kept[c.ExternalID] = true
			continue
		}
		comparable = append(comparable, parsedCandidate{candidate: c, prefix: prefix})
	}

	dropped := make(map[string]bool, len(comparable))
	for i := 0; i < len(comparable); i++ {
		for j := i + 1; j < len(comparable); j++ {
			a, b := comparable[i], comparable[j]
			if dropped[a.candidate.ExternalID] || dropped[b.candidate.ExternalID] {
				continue
			}
			if !a.prefix.Overlaps(b.prefix) {
				continue
			}
			switch {
			case a.prefix.Bits() > b.prefix.Bits():
				// a is more specific; b's broader range is superseded.
				dropped[b.candidate.ExternalID] = true
				log.Info("ip group superseded by more specific range",
					"dropped_cidr", b.candidate.CIDR, "dropped_external_id", b.candidate.ExternalID,
					"kept_cidr", a.candidate.CIDR, "kept_external_id", a.candidate.ExternalID)
			case b.prefix.Bits() > a.prefix.Bits():
				dropped[a.candidate.ExternalID] = true
				log.Info("ip group superseded by more specific range",
					"dropped_cidr", a.candidate.CIDR, "dropped_external_id", a.candidate.ExternalID,
					"kept_cidr", b.candidate.CIDR, "kept_external_id", b.candidate.ExternalID)
			default:
				winner, loser := a.candidate, b.candidate
				if tieKey(winner) > tieKey(loser) {
					winner, loser = loser, winner
				}
				dropped[loser.ExternalID] = true
				log.Warn("ip group CIDR collision, dropping duplicate",
					"cidr", loser.CIDR,
					"kept_tenant", winner.Tenant, "kept_vrf", winner.VRF, "kept_external_id", winner.ExternalID,
					"dropped_tenant", loser.Tenant, "dropped_vrf", loser.VRF, "dropped_external_id", loser.ExternalID,
					"reason", "no containment relationship; picked deterministically by tenant/vrf/cidr")
			}
		}
	}

	for _, p := range comparable {
		if !dropped[p.candidate.ExternalID] {
			kept[p.candidate.ExternalID] = true
		}
	}
	return kept
}

// tieKey orders candidates for deterministic tie-break: alphabetically
// first Tenant wins, then VRF, then CIDR, then ExternalID as a final
// tiebreaker to guarantee a total order regardless of input ordering.
func tieKey(c ipGroupCandidate) string {
	return c.Tenant + "\x00" + c.VRF + "\x00" + c.CIDR + "\x00" + c.ExternalID
}
