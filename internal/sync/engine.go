// Package sync implements the core fetch -> diff -> apply -> report loop
// that both the scheduler and the one-shot CLI run mode invoke. It is
// schedule-agnostic: Engine.RunJob performs exactly one sync pass and
// returns, with no ticking or looping of its own.
package sync

import (
	"context"
	"fmt"
	"time"

	"github.com/kentikethan/kentik_sync_agent/internal/core"
	"github.com/kentikethan/kentik_sync_agent/internal/destination/kentik"
	"github.com/kentikethan/kentik_sync_agent/internal/source"
	"github.com/kentikethan/kentik_sync_agent/internal/state"
)

// Job is one sync pass's parameters: a configured source, which object
// types to sync from it, and how.
type Job struct {
	SourceName            string
	Source                source.Source
	Objects               []core.ObjectType
	Incremental           bool
	FullReconcileInterval time.Duration
	// DryRun computes the full create/update/delete plan and reports it,
	// but calls neither Kentik nor the state store — the primary tool for
	// validating a new source config before it touches real data.
	DryRun bool
}

func (j Job) wants(obj core.ObjectType) bool {
	for _, o := range j.Objects {
		if o == obj {
			return true
		}
	}
	return false
}

// fullReconcileMarkerObj reuses the state store's cursor mechanism (see
// internal/state) to track when each object type last ran a full
// reconciliation pass, without needing a separate schema/table for it.
func fullReconcileMarkerObj(obj core.ObjectType) core.ObjectType {
	return core.ObjectType(string(obj) + ":full_reconcile_marker")
}

// Engine wires the state store to the Kentik destination appliers and runs
// sync jobs against them.
type Engine struct {
	Store    state.Store
	Sites    *kentik.SiteApplier
	Devices  *kentik.DeviceApplier
	IPGroups *kentik.PopulatorApplier
	Labels   *kentik.LabelApplier
}

// resolveSince decides, for one object type in a job, what `since` cursor
// to fetch with and whether this is a full reconciliation run. A run is
// full when: the job isn't configured for incremental sync, the source
// doesn't support it, there's no prior cursor (first run), or the
// configured full_reconcile_interval has elapsed since the last full run.
func (e *Engine) resolveSince(ctx context.Context, job Job, obj core.ObjectType) (since string, isFullRun bool, err error) {
	if !job.Incremental || !job.Source.SupportsIncremental() {
		return "", true, nil
	}

	lastFull, ok, err := e.Store.GetCursor(ctx, job.SourceName, fullReconcileMarkerObj(obj))
	if err != nil {
		return "", false, fmt.Errorf("sync: reading full-reconcile marker: %w", err)
	}
	if !ok {
		return "", true, nil
	}
	lastFullTime, err := time.Parse(time.RFC3339, lastFull)
	if err != nil || time.Since(lastFullTime) >= job.FullReconcileInterval {
		return "", true, nil
	}

	cursor, ok, err := e.Store.GetCursor(ctx, job.SourceName, obj)
	if err != nil {
		return "", false, fmt.Errorf("sync: reading cursor: %w", err)
	}
	if !ok {
		return "", true, nil
	}
	return cursor, false, nil
}

// commitRun persists the new incremental cursor and, on a full run, the
// full-reconcile marker. Skipped entirely in dry-run mode.
func (e *Engine) commitRun(ctx context.Context, job Job, obj core.ObjectType, newCursor string, isFullRun bool) error {
	if job.DryRun {
		return nil
	}
	if newCursor != "" {
		if err := e.Store.SetCursor(ctx, job.SourceName, obj, newCursor); err != nil {
			return fmt.Errorf("sync: saving cursor: %w", err)
		}
	}
	if isFullRun {
		if err := e.Store.SetCursor(ctx, job.SourceName, fullReconcileMarkerObj(obj), time.Now().UTC().Format(time.RFC3339)); err != nil {
			return fmt.Errorf("sync: saving full-reconcile marker: %w", err)
		}
	}
	return nil
}

// siteKentikIDs builds an externalSiteID -> Kentik numeric site ID map from
// every site mapping known to the state store for this source, so device
// sync can resolve site references regardless of which run created them.
func (e *Engine) siteKentikIDs(ctx context.Context, sourceName string) (map[string]uint32, error) {
	mappings, err := e.Store.ListMappings(ctx, sourceName, core.ObjectSites)
	if err != nil {
		return nil, fmt.Errorf("sync: listing known sites: %w", err)
	}
	out := make(map[string]uint32, len(mappings))
	for _, m := range mappings {
		id, err := parseUint32(m.KentikID)
		if err != nil {
			continue // a malformed stored ID shouldn't abort the whole device run
		}
		out[m.ExternalID] = id
	}
	return out, nil
}

// deviceKentikIDs builds an externalDeviceID -> Kentik device ID map from
// every device mapping known to the state store for this source, so device
// label sync can resolve which Kentik device to write labels onto
// regardless of which run created it.
func (e *Engine) deviceKentikIDs(ctx context.Context, sourceName string) (map[string]string, error) {
	mappings, err := e.Store.ListMappings(ctx, sourceName, core.ObjectDevices)
	if err != nil {
		return nil, fmt.Errorf("sync: listing known devices: %w", err)
	}
	out := make(map[string]string, len(mappings))
	for _, m := range mappings {
		out[m.ExternalID] = m.KentikID
	}
	return out, nil
}

// RunJob performs exactly one sync pass for job. Sites and devices and IP
// groups are created/updated in that order (devices need their site synced
// first to resolve a Kentik site_id; IP groups have no such dependency),
// then device labels (which need a device's Kentik ID already synced), then
// deleted in the reverse order, so a device is never left referencing a
// deleted site and a site is never deleted while a device still points at
// it.
func (e *Engine) RunJob(ctx context.Context, job Job) (Result, error) {
	result := Result{SourceName: job.SourceName}

	var sitesToDelete, devicesToDelete, deviceLabelsToDelete map[string]string

	if job.wants(core.ObjectSites) {
		r, toDelete, err := e.runSites(ctx, job)
		if err != nil {
			result.FetchErrors = append(result.FetchErrors, err)
		} else {
			result.Sites = r
			sitesToDelete = toDelete
		}
	}

	if job.wants(core.ObjectDevices) {
		r, toDelete, err := e.runDevices(ctx, job)
		if err != nil {
			result.FetchErrors = append(result.FetchErrors, err)
		} else {
			result.Devices = r
			devicesToDelete = toDelete
		}
	}

	if job.wants(core.ObjectIPGroups) {
		r, err := e.runIPGroups(ctx, job)
		if err != nil {
			result.FetchErrors = append(result.FetchErrors, err)
		} else {
			result.IPGroups = r
		}
	}

	if job.wants(core.ObjectDeviceLabels) {
		r, toDelete, err := e.runDeviceLabels(ctx, job)
		if err != nil {
			result.FetchErrors = append(result.FetchErrors, err)
		} else {
			result.DeviceLabels = r
			deviceLabelsToDelete = toDelete
		}
	}

	if job.wants(core.ObjectDeviceLabels) {
		e.deleteDeviceLabels(ctx, job, deviceLabelsToDelete, &result.DeviceLabels)
	}
	if job.wants(core.ObjectDevices) {
		e.deleteDevices(ctx, job, devicesToDelete, &result.Devices)
	}
	if job.wants(core.ObjectSites) {
		e.deleteSites(ctx, job, sitesToDelete, &result.Sites)
	}

	return result, nil
}

func parseUint32(s string) (uint32, error) {
	var v uint64
	_, err := fmt.Sscanf(s, "%d", &v)
	if err != nil {
		return 0, err
	}
	return uint32(v), nil
}
