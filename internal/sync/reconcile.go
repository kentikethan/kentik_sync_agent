package sync

import (
	"context"
	"fmt"

	"github.com/kentikethan/kentik_sync_agent/internal/core"
	"github.com/kentikethan/kentik_sync_agent/internal/state"
)

// mappingFor builds the state.Mapping row to persist after successfully
// creating or updating item in Kentik.
func mappingFor[T any](sourceName string, obj core.ObjectType, externalID, kentikID string, item T) state.Mapping {
	return state.Mapping{
		SourceName:  sourceName,
		ObjectType:  obj,
		ExternalID:  externalID,
		KentikID:    kentikID,
		ContentHash: contentHash(item),
	}
}

// plan is the outcome of diffing a source's fetched items against the local
// state store: what to create, what to update (with its known Kentik ID),
// and what to delete (external ID -> known Kentik ID).
type plan[T any] struct {
	toCreate       []T
	toUpdate       []T
	updateKentikID map[string]string // externalID -> kentikID, for items in toUpdate
	toDelete       map[string]string // externalID -> kentikID
}

// diff compares a fetch result against the local state store's record of
// what was previously synced for (sourceName, obj), deciding create/update/
// skip for each fetched item and, for a full run (or explicitly reported
// deletions), which previously-synced items are now gone.
//
// A full run is one where isFullRun is true: either the source doesn't
// support incremental fetches, or this is a periodic full-reconcile pass.
// Only full runs (or explicit fetched.Deleted entries) trigger deletes —
// an incremental run that simply didn't mention an object says nothing
// about whether that object still exists.
func diff[T any](
	ctx context.Context,
	store state.Store,
	sourceName string,
	obj core.ObjectType,
	fetched core.FetchResult[T],
	externalIDOf func(T) string,
	isFullRun bool,
) (plan[T], error) {
	p := plan[T]{
		updateKentikID: map[string]string{},
		toDelete:       map[string]string{},
	}

	seen := map[string]bool{}
	for _, item := range fetched.Items {
		extID := externalIDOf(item)
		seen[extID] = true

		existing, ok, err := store.GetMapping(ctx, sourceName, obj, extID)
		if err != nil {
			return plan[T]{}, fmt.Errorf("sync: looking up state for %s %s: %w", obj, extID, err)
		}
		hash := contentHash(item)
		if !ok {
			p.toCreate = append(p.toCreate, item)
			continue
		}
		if existing.ContentHash != hash {
			p.toUpdate = append(p.toUpdate, item)
			p.updateKentikID[extID] = existing.KentikID
		}
		// else: unchanged, nothing to do.
	}

	for _, extID := range fetched.Deleted {
		if extID == "" {
			continue
		}
		if existing, ok, err := store.GetMapping(ctx, sourceName, obj, extID); err != nil {
			return plan[T]{}, fmt.Errorf("sync: looking up state for deleted %s %s: %w", obj, extID, err)
		} else if ok {
			p.toDelete[extID] = existing.KentikID
		}
	}

	if isFullRun {
		known, err := store.ListMappings(ctx, sourceName, obj)
		if err != nil {
			return plan[T]{}, fmt.Errorf("sync: listing known %s mappings: %w", obj, err)
		}
		for _, m := range known {
			if !seen[m.ExternalID] {
				p.toDelete[m.ExternalID] = m.KentikID
			}
		}
	}

	return p, nil
}
