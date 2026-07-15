package sync

import (
	"context"
	"fmt"

	"github.com/kentikethan/kentik_sync_agent/internal/core"
)

// runDevices fetches, diffs, and applies creates/updates for a job's
// devices. Deletes are computed but not applied here — see deleteDevices —
// so devices referencing a to-be-deleted site are removed before the site
// itself is.
func (e *Engine) runDevices(ctx context.Context, job Job) (result ObjectResult, toDelete map[string]string, err error) {
	since, isFullRun, err := e.resolveSince(ctx, job, core.ObjectDevices)
	if err != nil {
		return ObjectResult{}, nil, err
	}

	fetched, err := job.Source.FetchDevices(ctx, since)
	if err != nil {
		return ObjectResult{}, nil, fmt.Errorf("sync: fetching devices from %s: %w", job.SourceName, err)
	}
	for i := range fetched.Items {
		fetched.Items[i].SourcePlugin = job.SourceName
	}

	p, err := diff(ctx, e.Store, job.SourceName, core.ObjectDevices, fetched, func(d core.Device) string { return d.ExternalID }, isFullRun)
	if err != nil {
		return ObjectResult{}, nil, err
	}

	if job.DryRun {
		result.Created = len(p.toCreate)
		result.Updated = len(p.toUpdate)
		result.Deleted = len(p.toDelete)
		return result, nil, nil
	}

	siteIDs, err := e.siteKentikIDs(ctx, job.SourceName)
	if err != nil {
		return ObjectResult{}, nil, err
	}

	created, failedCreate := e.Devices.Create(ctx, p.toCreate, siteIDs)
	for _, d := range p.toCreate {
		if kentikID, ok := created[d.ExternalID]; ok {
			if err := e.Store.UpsertMapping(ctx, mappingFor(job.SourceName, core.ObjectDevices, d.ExternalID, kentikID, d)); err != nil {
				result.addFailure(fmt.Errorf("sync: saving device mapping %s: %w", d.ExternalID, err))
				continue
			}
			result.Created++
		}
	}
	for _, err := range failedCreate {
		result.addFailure(err)
	}

	updated, failedUpdate := e.Devices.Update(ctx, p.toUpdate, siteIDs, p.updateKentikID)
	for _, d := range p.toUpdate {
		if updated[d.ExternalID] {
			kentikID := p.updateKentikID[d.ExternalID]
			if err := e.Store.UpsertMapping(ctx, mappingFor(job.SourceName, core.ObjectDevices, d.ExternalID, kentikID, d)); err != nil {
				result.addFailure(fmt.Errorf("sync: saving device mapping %s: %w", d.ExternalID, err))
				continue
			}
			result.Updated++
		}
	}
	for _, err := range failedUpdate {
		result.addFailure(err)
	}

	if err := e.commitRun(ctx, job, core.ObjectDevices, fetched.Cursor, isFullRun); err != nil {
		result.addFailure(err)
	}

	return result, p.toDelete, nil
}

// deleteDevices applies a previously-computed device deletion plan.
func (e *Engine) deleteDevices(ctx context.Context, job Job, toDelete map[string]string, result *ObjectResult) {
	if job.DryRun || len(toDelete) == 0 {
		return
	}
	kentikIDs := make([]string, 0, len(toDelete))
	byKentikID := make(map[string]string, len(toDelete))
	for extID, kentikID := range toDelete {
		kentikIDs = append(kentikIDs, kentikID)
		byKentikID[kentikID] = extID
	}

	deleted, failed := e.Devices.Delete(ctx, kentikIDs)
	for kentikID := range deleted {
		extID := byKentikID[kentikID]
		if err := e.Store.DeleteMapping(ctx, job.SourceName, core.ObjectDevices, extID); err != nil {
			result.addFailure(fmt.Errorf("sync: removing device mapping %s: %w", extID, err))
			continue
		}
		result.Deleted++
	}
	for _, err := range failed {
		result.addFailure(err)
	}
}
