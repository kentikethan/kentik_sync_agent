package sync

import (
	"context"
	"fmt"

	"github.com/kentikethan/kentik_sync_agent/internal/core"
)

// runDeviceLabels fetches, diffs, and applies device label sets for a job.
// It requires devices to already be synced (core.ObjectDevices in the same
// job, run earlier by RunJob) so it can resolve each item's Kentik device
// ID; an item whose device isn't known yet is reported as a failure rather
// than silently skipped.
func (e *Engine) runDeviceLabels(ctx context.Context, job Job) (result ObjectResult, toDelete map[string]string, err error) {
	since, isFullRun, err := e.resolveSince(ctx, job, core.ObjectDeviceLabels)
	if err != nil {
		return ObjectResult{}, nil, err
	}

	fetched, err := job.Source.FetchDeviceLabels(ctx, since)
	if err != nil {
		return ObjectResult{}, nil, fmt.Errorf("sync: fetching device labels from %s: %w", job.SourceName, err)
	}
	for i := range fetched.Items {
		fetched.Items[i].SourcePlugin = job.SourceName
	}

	p, err := diff(ctx, e.Store, job.SourceName, core.ObjectDeviceLabels, fetched, func(d core.DeviceLabels) string { return d.ExternalID }, isFullRun)
	if err != nil {
		return ObjectResult{}, nil, err
	}

	if job.DryRun {
		result.Created = len(p.toCreate)
		result.Updated = len(p.toUpdate)
		result.Deleted = len(p.toDelete)
		return result, nil, nil
	}

	deviceIDs, err := e.deviceKentikIDs(ctx, job.SourceName)
	if err != nil {
		return ObjectResult{}, nil, err
	}

	applied, failedCreate := e.Labels.Apply(ctx, p.toCreate, deviceIDs)
	for _, d := range p.toCreate {
		if applied[d.ExternalID] {
			if err := e.Store.UpsertMapping(ctx, mappingFor(job.SourceName, core.ObjectDeviceLabels, d.ExternalID, deviceIDs[d.ExternalID], d)); err != nil {
				result.addFailure(fmt.Errorf("sync: saving device label mapping %s: %w", d.ExternalID, err))
				continue
			}
			result.Created++
		}
	}
	for _, err := range failedCreate {
		result.addFailure(err)
	}

	updated, failedUpdate := e.Labels.Apply(ctx, p.toUpdate, deviceIDs)
	for _, d := range p.toUpdate {
		if updated[d.ExternalID] {
			if err := e.Store.UpsertMapping(ctx, mappingFor(job.SourceName, core.ObjectDeviceLabels, d.ExternalID, deviceIDs[d.ExternalID], d)); err != nil {
				result.addFailure(fmt.Errorf("sync: saving device label mapping %s: %w", d.ExternalID, err))
				continue
			}
			result.Updated++
		}
	}
	for _, err := range failedUpdate {
		result.addFailure(err)
	}

	if err := e.commitRun(ctx, job, core.ObjectDeviceLabels, fetched.Cursor, isFullRun); err != nil {
		result.addFailure(err)
	}

	return result, p.toDelete, nil
}

// deleteDeviceLabels clears this agent's managed labels from devices whose
// label set disappeared from the source (typically because the device
// itself was deleted from NetBox — deleteDevices, run right after this,
// removes the Kentik device entirely in that case).
func (e *Engine) deleteDeviceLabels(ctx context.Context, job Job, toDelete map[string]string, result *ObjectResult) {
	if job.DryRun || len(toDelete) == 0 {
		return
	}
	cleared, failed := e.Labels.Clear(ctx, toDelete)
	for extID := range cleared {
		if err := e.Store.DeleteMapping(ctx, job.SourceName, core.ObjectDeviceLabels, extID); err != nil {
			result.addFailure(fmt.Errorf("sync: removing device label mapping %s: %w", extID, err))
			continue
		}
		result.Deleted++
	}
	for _, err := range failed {
		result.addFailure(err)
	}
}
