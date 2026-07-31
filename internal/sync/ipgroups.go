package sync

import (
	"context"
	"fmt"

	"github.com/kentikethan/kentik_sync_agent/internal/core"
	"github.com/kentikethan/kentik_sync_agent/internal/state"
)

// ipGroupBucket accumulates the create/update/delete plan for every IP
// group targeting one Kentik Custom Dimension.
type ipGroupBucket struct {
	dimensionID    string
	toCreate       []core.IPGroup
	toUpdate       []core.IPGroup
	updateKentikID map[string]string // externalID -> kentikID
	toDelete       map[string]string // externalID -> kentikID
	kept           map[string]bool   // populated by containment filtering
}

// ipGroupMigration tracks an IP group whose resolved dimension changed
// since it was last synced (a tenant/VRF change, or a config edit to
// ip_group_dimensions) — it needs a fresh populator in its new dimension
// and cleanup of the stale one in its old dimension, but the old cleanup
// must only happen once the new create is confirmed, and never touches
// local state directly (see runIPGroups for why).
type ipGroupMigration struct {
	externalID     string
	oldDimensionID string
	oldKentikID    string
	newDimensionID string
}

func newIPGroupBucket(dimensionID string) *ipGroupBucket {
	return &ipGroupBucket{
		dimensionID:    dimensionID,
		updateKentikID: map[string]string{},
		toDelete:       map[string]string{},
	}
}

// runIPGroups fetches, resolves each item's target Kentik Custom Dimension,
// filters overlapping CIDRs down to the most specific per dimension, and
// applies creates/updates/deletes per dimension (Kentik Populators). IP
// groups have no cross-object-type dependency, so deletes are applied in
// the same pass rather than deferred.
//
// Unlike sites/devices/labels (which use the generic diff() helper), IP
// groups need two things diff() can't express: (1) an item whose target
// dimension changed must move — delete from its old dimension, create in
// its new one — rather than simply update in place, and (2) within a
// dimension, an existing synced item can be superseded by a newly
// overlapping, more-specific item that just appeared, even on an
// incremental run. Both require comparing every fetched item against the
// full previously-synced set (loaded once from local state below, which is
// cheap — no extra NetBox calls), not just a fetched-vs-state lookup keyed
// by ExternalID alone.
func (e *Engine) runIPGroups(ctx context.Context, job Job) (result ObjectResult, err error) {
	since, isFullRun, err := e.resolveSince(ctx, job, core.ObjectIPGroups)
	if err != nil {
		return ObjectResult{}, err
	}

	fetched, err := job.Source.FetchIPGroups(ctx, since)
	if err != nil {
		return ObjectResult{}, fmt.Errorf("sync: fetching IP groups from %s: %w", job.SourceName, err)
	}
	for i := range fetched.Items {
		fetched.Items[i].SourcePlugin = job.SourceName
	}

	knownList, err := e.Store.ListMappings(ctx, job.SourceName, core.ObjectIPGroups)
	if err != nil {
		return ObjectResult{}, fmt.Errorf("sync: listing known IP groups: %w", err)
	}
	known := make(map[string]state.Mapping, len(knownList))
	for _, m := range knownList {
		known[m.ExternalID] = m
	}

	buckets := map[string]*ipGroupBucket{}
	bucket := func(dimensionID string) *ipGroupBucket {
		b, ok := buckets[dimensionID]
		if !ok {
			b = newIPGroupBucket(dimensionID)
			buckets[dimensionID] = b
		}
		return b
	}

	var migrations []*ipGroupMigration
	migrationByExtID := map[string]*ipGroupMigration{}

	seen := map[string]bool{}
	for _, g := range fetched.Items {
		// An "either"-direction group targets both the source and
		// destination dimensions as two independent populators (Kentik
		// populators only match one traffic side each), so it's split into
		// two distinctly-tracked items — suffixed ExternalIDs so each has
		// its own state.Mapping row. A single-direction group keeps its
		// original ExternalID unchanged.
		targets := e.IPGroups.ResolveTargets(g.Tenant, g.VRF, g.Direction)
		sideLabels := []string{""}
		sideDirections := []core.IPGroupDirection{g.Direction}
		if len(targets) == 2 {
			sideLabels = []string{"src", "dst"}
			sideDirections = []core.IPGroupDirection{core.IPGroupDirectionSrc, core.IPGroupDirectionDst}
		}

		for i, desired := range targets {
			item := g
			externalID := g.ExternalID
			if sideLabels[i] != "" {
				externalID = g.ExternalID + ":" + sideLabels[i]
				item.ExternalID = externalID
				item.Direction = sideDirections[i]
			}
			seen[externalID] = true

			m, ok := known[externalID]
			switch {
			case !ok:
				bucket(desired).toCreate = append(bucket(desired).toCreate, item)
			case m.CustomDimensionID != desired:
				bucket(desired).toCreate = append(bucket(desired).toCreate, item)
				mig := &ipGroupMigration{
					externalID:     externalID,
					oldDimensionID: m.CustomDimensionID,
					oldKentikID:    m.KentikID,
					newDimensionID: desired,
				}
				migrations = append(migrations, mig)
				migrationByExtID[externalID] = mig
			case contentHash(item) != m.ContentHash:
				b := bucket(desired)
				b.toUpdate = append(b.toUpdate, item)
				b.updateKentikID[externalID] = m.KentikID
				// else: unchanged, same dimension — nothing to do.
			}
		}
	}

	for _, extID := range fetched.Deleted {
		if extID == "" {
			continue
		}
		if m, ok := known[extID]; ok {
			bucket(m.CustomDimensionID).toDelete[extID] = m.KentikID
		}
	}

	if isFullRun {
		for extID, m := range known {
			if !seen[extID] {
				bucket(m.CustomDimensionID).toDelete[extID] = m.KentikID
			}
		}
	}

	// Containment filtering, per dimension: gather every IP group that
	// would otherwise end up live in this dimension after this run — fresh
	// creates/updates, plus previously-synced items this run doesn't touch
	// — and keep only the most specific per overlapping range.
	for dimensionID, b := range buckets {
		var candidates []ipGroupCandidate
		inBucket := map[string]bool{}
		for _, g := range b.toCreate {
			candidates = append(candidates, ipGroupCandidate{ExternalID: g.ExternalID, CIDR: g.CIDR, Tenant: g.Tenant, VRF: g.VRF})
			inBucket[g.ExternalID] = true
		}
		for _, g := range b.toUpdate {
			candidates = append(candidates, ipGroupCandidate{ExternalID: g.ExternalID, CIDR: g.CIDR, Tenant: g.Tenant, VRF: g.VRF})
			inBucket[g.ExternalID] = true
		}
		for extID, m := range known {
			if m.CustomDimensionID != dimensionID || inBucket[extID] {
				continue
			}
			if _, isDelete := b.toDelete[extID]; isDelete {
				continue
			}
			if mig, isMigrating := migrationByExtID[extID]; isMigrating && mig.oldDimensionID == dimensionID {
				continue // leaving this dimension; handled via migrations below
			}
			candidates = append(candidates, ipGroupCandidate{ExternalID: extID, CIDR: m.CIDR, Tenant: m.Tenant, VRF: m.VRF})
		}

		b.kept = filterMostSpecific(candidates, e.logger())

		var survivingCreate []core.IPGroup
		for _, g := range b.toCreate {
			if b.kept[g.ExternalID] {
				survivingCreate = append(survivingCreate, g)
			}
		}
		b.toCreate = survivingCreate

		var survivingUpdate []core.IPGroup
		for _, g := range b.toUpdate {
			if b.kept[g.ExternalID] {
				survivingUpdate = append(survivingUpdate, g)
			} else {
				delete(b.updateKentikID, g.ExternalID)
			}
		}
		b.toUpdate = survivingUpdate

		for extID, m := range known {
			if m.CustomDimensionID != dimensionID || inBucket[extID] {
				continue
			}
			if _, isDelete := b.toDelete[extID]; isDelete {
				continue
			}
			if mig, isMigrating := migrationByExtID[extID]; isMigrating && mig.oldDimensionID == dimensionID {
				continue
			}
			if !b.kept[extID] {
				b.toDelete[extID] = m.KentikID
			}
		}
	}

	// A migrating item that lost containment in its new dimension has no
	// live populator anywhere after this run — fold its old populator into
	// a plain delete (state row removed too) rather than the create-first
	// migration cleanup below.
	var liveMigrations []*ipGroupMigration
	for _, mig := range migrations {
		if buckets[mig.newDimensionID].kept[mig.externalID] {
			liveMigrations = append(liveMigrations, mig)
		} else {
			bucket(mig.oldDimensionID).toDelete[mig.externalID] = mig.oldKentikID
		}
	}

	if job.DryRun {
		for _, b := range buckets {
			result.Created += len(b.toCreate)
			result.Updated += len(b.toUpdate)
			result.Deleted += len(b.toDelete)
		}
		return result, nil
	}

	createdThisRun := map[string]bool{}

	for _, b := range buckets {
		applier := e.IPGroups.Applier(b.dimensionID)
		if applier == nil {
			result.addFailure(fmt.Errorf("sync: no Kentik applier configured for custom dimension %q", b.dimensionID))
			continue
		}

		created, failedCreate := applier.Create(ctx, b.toCreate)
		for _, g := range b.toCreate {
			kentikID, ok := created[g.ExternalID]
			if !ok {
				continue
			}
			m := mappingFor(job.SourceName, core.ObjectIPGroups, g.ExternalID, kentikID, g)
			m.CIDR, m.Tenant, m.VRF, m.CustomDimensionID = g.CIDR, g.Tenant, g.VRF, b.dimensionID
			if err := e.Store.UpsertMapping(ctx, m); err != nil {
				result.addFailure(fmt.Errorf("sync: saving IP group mapping %s: %w", g.ExternalID, err))
				continue
			}
			result.Created++
			createdThisRun[g.ExternalID] = true
		}
		for _, err := range failedCreate {
			result.addFailure(err)
		}

		updated, failedUpdate := applier.Update(ctx, b.toUpdate, b.updateKentikID)
		for _, g := range b.toUpdate {
			if !updated[g.ExternalID] {
				continue
			}
			kentikID := b.updateKentikID[g.ExternalID]
			m := mappingFor(job.SourceName, core.ObjectIPGroups, g.ExternalID, kentikID, g)
			m.CIDR, m.Tenant, m.VRF, m.CustomDimensionID = g.CIDR, g.Tenant, g.VRF, b.dimensionID
			if err := e.Store.UpsertMapping(ctx, m); err != nil {
				result.addFailure(fmt.Errorf("sync: saving IP group mapping %s: %w", g.ExternalID, err))
				continue
			}
			result.Updated++
		}
		for _, err := range failedUpdate {
			result.addFailure(err)
		}

		if len(b.toDelete) > 0 {
			deleted, failedDelete := applier.Delete(ctx, b.toDelete)
			for extID := range deleted {
				if err := e.Store.DeleteMapping(ctx, job.SourceName, core.ObjectIPGroups, extID); err != nil {
					result.addFailure(fmt.Errorf("sync: removing IP group mapping %s: %w", extID, err))
					continue
				}
				result.Deleted++
			}
			for _, err := range failedDelete {
				result.addFailure(err)
			}
		}
	}

	// Clean up the stale populator left behind by a completed migration.
	// Only for items confirmed created in their new dimension this run —
	// state already reflects the new dimension via that create's
	// UpsertMapping above, so this must NOT touch the state store: doing so
	// would race with (and could overwrite) that same row.
	byOldDimension := map[string]map[string]string{}
	for _, mig := range liveMigrations {
		if !createdThisRun[mig.externalID] {
			continue // new-dimension create failed; retry the whole migration next run
		}
		if byOldDimension[mig.oldDimensionID] == nil {
			byOldDimension[mig.oldDimensionID] = map[string]string{}
		}
		byOldDimension[mig.oldDimensionID][mig.externalID] = mig.oldKentikID
	}
	for oldDimensionID, toDelete := range byOldDimension {
		applier := e.IPGroups.Applier(oldDimensionID)
		if applier == nil {
			continue
		}
		deleted, failedDelete := applier.Delete(ctx, toDelete)
		result.Deleted += len(deleted)
		for _, err := range failedDelete {
			result.addFailure(fmt.Errorf("sync: cleaning up migrated IP group's old populator: %w", err))
		}
	}

	if err := e.commitRun(ctx, job, core.ObjectIPGroups, fetched.Cursor, isFullRun); err != nil {
		result.addFailure(err)
	}

	return result, nil
}
