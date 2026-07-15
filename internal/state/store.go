// Package state persists the mapping between a source's ExternalID and the
// Kentik-assigned ID for every synced object, plus incremental fetch
// cursors. Kentik's Device/Site/Populator objects have no queryable
// custom-field or label slot suitable for stashing an external-id marker
// (see docs/plugins.md for the investigation), so unlike a typical
// cache-only design, this store is the *authoritative* record of what this
// agent has previously created in Kentik — not just a performance
// optimization. Losing it means the agent can no longer recognize objects
// it created on a prior run; back up state.path accordingly.
package state

import (
	"context"
	"time"

	"github.com/kentikethan/kentik_sync_agent/internal/core"
)

// Mapping records one previously-synced object's identity and last-synced
// content hash (used to skip no-op updates).
type Mapping struct {
	SourceName  string
	ObjectType  core.ObjectType
	ExternalID  string
	KentikID    string
	ContentHash string
	UpdatedAt   time.Time
}

// Store is implemented by the SQLite-backed store (and an in-memory store
// used by tests / --dry-run).
type Store interface {
	GetCursor(ctx context.Context, sourceName string, obj core.ObjectType) (cursor string, ok bool, err error)
	SetCursor(ctx context.Context, sourceName string, obj core.ObjectType, cursor string) error

	GetMapping(ctx context.Context, sourceName string, obj core.ObjectType, externalID string) (Mapping, bool, error)
	UpsertMapping(ctx context.Context, m Mapping) error
	DeleteMapping(ctx context.Context, sourceName string, obj core.ObjectType, externalID string) error
	ListMappings(ctx context.Context, sourceName string, obj core.ObjectType) ([]Mapping, error)

	Close() error
}
