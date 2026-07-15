package state

import (
	"context"
	"sync"
	"time"

	"github.com/kentikethan/kentik_sync_agent/internal/core"
)

type mappingKey struct {
	source string
	obj    core.ObjectType
	extID  string
}

type cursorKey struct {
	source string
	obj    core.ObjectType
}

// MemoryStore is an in-memory Store, used by --dry-run (so a dry run never
// touches the real state file) and by tests.
type MemoryStore struct {
	mu       sync.RWMutex
	mappings map[mappingKey]Mapping
	cursors  map[cursorKey]string
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		mappings: map[mappingKey]Mapping{},
		cursors:  map[cursorKey]string{},
	}
}

func (s *MemoryStore) Close() error { return nil }

func (s *MemoryStore) GetCursor(_ context.Context, sourceName string, obj core.ObjectType) (string, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.cursors[cursorKey{sourceName, obj}]
	return c, ok, nil
}

func (s *MemoryStore) SetCursor(_ context.Context, sourceName string, obj core.ObjectType, cursor string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cursors[cursorKey{sourceName, obj}] = cursor
	return nil
}

func (s *MemoryStore) GetMapping(_ context.Context, sourceName string, obj core.ObjectType, externalID string) (Mapping, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, ok := s.mappings[mappingKey{sourceName, obj, externalID}]
	return m, ok, nil
}

func (s *MemoryStore) UpsertMapping(_ context.Context, m Mapping) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m.UpdatedAt = time.Now().UTC()
	s.mappings[mappingKey{m.SourceName, m.ObjectType, m.ExternalID}] = m
	return nil
}

func (s *MemoryStore) DeleteMapping(_ context.Context, sourceName string, obj core.ObjectType, externalID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.mappings, mappingKey{sourceName, obj, externalID})
	return nil
}

func (s *MemoryStore) ListMappings(_ context.Context, sourceName string, obj core.ObjectType) ([]Mapping, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Mapping
	for k, m := range s.mappings {
		if k.source == sourceName && k.obj == obj {
			out = append(out, m)
		}
	}
	return out, nil
}
