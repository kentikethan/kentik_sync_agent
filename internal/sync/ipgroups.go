package sync

import (
	"context"
	"fmt"

	"github.com/kentikethan/kentik_sync_agent/internal/core"
)

// runIPGroups fetches, diffs, and applies creates/updates for a job's IP
// groups (Kentik Populators). IP groups have no cross-object-type
// dependency, so deletes are applied in the same pass rather than deferred.
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

	p, err := diff(ctx, e.Store, job.SourceName, core.ObjectIPGroups, fetched, func(g core.IPGroup) string { return g.ExternalID }, isFullRun)
	if err != nil {
		return ObjectResult{}, err
	}

	if job.DryRun {
		result.Created = len(p.toCreate)
		result.Updated = len(p.toUpdate)
		result.Deleted = len(p.toDelete)
		return result, nil
	}

	created, failedCreate := e.IPGroups.Create(ctx, p.toCreate)
	for _, g := range p.toCreate {
		if kentikID, ok := created[g.ExternalID]; ok {
			if err := e.Store.UpsertMapping(ctx, mappingFor(job.SourceName, core.ObjectIPGroups, g.ExternalID, kentikID, g)); err != nil {
				result.addFailure(fmt.Errorf("sync: saving IP group mapping %s: %w", g.ExternalID, err))
				continue
			}
			result.Created++
		}
	}
	for _, err := range failedCreate {
		result.addFailure(err)
	}

	updated, failedUpdate := e.IPGroups.Update(ctx, p.toUpdate, p.updateKentikID)
	for _, g := range p.toUpdate {
		if updated[g.ExternalID] {
			kentikID := p.updateKentikID[g.ExternalID]
			if err := e.Store.UpsertMapping(ctx, mappingFor(job.SourceName, core.ObjectIPGroups, g.ExternalID, kentikID, g)); err != nil {
				result.addFailure(fmt.Errorf("sync: saving IP group mapping %s: %w", g.ExternalID, err))
				continue
			}
			result.Updated++
		}
	}
	for _, err := range failedUpdate {
		result.addFailure(err)
	}

	if len(p.toDelete) > 0 {
		deleted, failedDelete := e.IPGroups.Delete(ctx, p.toDelete)
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

	if err := e.commitRun(ctx, job, core.ObjectIPGroups, fetched.Cursor, isFullRun); err != nil {
		result.addFailure(err)
	}

	return result, nil
}
