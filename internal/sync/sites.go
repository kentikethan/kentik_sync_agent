package sync

import (
	"context"
	"fmt"

	"github.com/kentikethan/kentik_sync_agent/internal/core"
)

// runSites fetches, diffs, and applies creates/updates for a job's sites.
// Deletes are computed but not applied here — see deleteSites — so the
// caller can defer site deletion until after any devices referencing those
// sites have themselves been deleted.
func (e *Engine) runSites(ctx context.Context, job Job) (result ObjectResult, toDelete map[string]string, err error) {
	since, isFullRun, err := e.resolveSince(ctx, job, core.ObjectSites)
	if err != nil {
		return ObjectResult{}, nil, err
	}

	fetched, err := job.Source.FetchSites(ctx, since)
	if err != nil {
		return ObjectResult{}, nil, fmt.Errorf("sync: fetching sites from %s: %w", job.SourceName, err)
	}
	for i := range fetched.Items {
		fetched.Items[i].SourcePlugin = job.SourceName
	}

	p, err := diff(ctx, e.Store, job.SourceName, core.ObjectSites, fetched, func(s core.Site) string { return s.ExternalID }, isFullRun)
	if err != nil {
		return ObjectResult{}, nil, err
	}

	if job.DryRun {
		result.Created = len(p.toCreate)
		result.Updated = len(p.toUpdate)
		result.Deleted = len(p.toDelete)
		return result, nil, nil
	}

	if created, failed := e.Sites.Create(ctx, p.toCreate); true {
		for _, s := range p.toCreate {
			if kentikID, ok := created[s.ExternalID]; ok {
				if err := e.Store.UpsertMapping(ctx, mappingFor(job.SourceName, core.ObjectSites, s.ExternalID, kentikID, s)); err != nil {
					result.addFailure(fmt.Errorf("sync: saving site mapping %s: %w", s.ExternalID, err))
					continue
				}
				result.Created++
			}
		}
		for _, err := range failed {
			result.addFailure(err)
		}
	}

	if updated, failed := e.Sites.Update(ctx, p.toUpdate, p.updateKentikID); true {
		for _, s := range p.toUpdate {
			if updated[s.ExternalID] {
				kentikID := p.updateKentikID[s.ExternalID]
				if err := e.Store.UpsertMapping(ctx, mappingFor(job.SourceName, core.ObjectSites, s.ExternalID, kentikID, s)); err != nil {
					result.addFailure(fmt.Errorf("sync: saving site mapping %s: %w", s.ExternalID, err))
					continue
				}
				result.Updated++
			}
		}
		for _, err := range failed {
			result.addFailure(err)
		}
	}

	if err := e.commitRun(ctx, job, core.ObjectSites, fetched.Cursor, isFullRun); err != nil {
		result.addFailure(err)
	}

	return result, p.toDelete, nil
}

// deleteSites applies a previously-computed site deletion plan.
func (e *Engine) deleteSites(ctx context.Context, job Job, toDelete map[string]string, result *ObjectResult) {
	if job.DryRun || len(toDelete) == 0 {
		return
	}
	deleted, failed := e.Sites.Delete(ctx, toDelete)
	for extID := range deleted {
		if err := e.Store.DeleteMapping(ctx, job.SourceName, core.ObjectSites, extID); err != nil {
			result.addFailure(fmt.Errorf("sync: removing site mapping %s: %w", extID, err))
			continue
		}
		result.Deleted++
	}
	for _, err := range failed {
		result.addFailure(err)
	}
}
