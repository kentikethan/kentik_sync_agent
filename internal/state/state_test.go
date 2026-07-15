package state

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/kentikethan/kentik_sync_agent/internal/core"
)

// runStoreTests exercises the Store interface's contract identically
// against every implementation, so SQLiteStore and MemoryStore can't drift.
func runStoreTests(t *testing.T, newStore func(t *testing.T) Store) {
	ctx := context.Background()

	t.Run("cursor round-trip", func(t *testing.T) {
		s := newStore(t)
		if _, ok, err := s.GetCursor(ctx, "netbox-primary", core.ObjectDevices); err != nil || ok {
			t.Fatalf("expected no cursor initially, got ok=%v err=%v", ok, err)
		}
		if err := s.SetCursor(ctx, "netbox-primary", core.ObjectDevices, "2026-01-01T00:00:00Z"); err != nil {
			t.Fatalf("SetCursor: %v", err)
		}
		cursor, ok, err := s.GetCursor(ctx, "netbox-primary", core.ObjectDevices)
		if err != nil || !ok || cursor != "2026-01-01T00:00:00Z" {
			t.Fatalf("expected stored cursor, got cursor=%q ok=%v err=%v", cursor, ok, err)
		}
		// overwrite
		if err := s.SetCursor(ctx, "netbox-primary", core.ObjectDevices, "2026-02-01T00:00:00Z"); err != nil {
			t.Fatalf("SetCursor overwrite: %v", err)
		}
		cursor, _, _ = s.GetCursor(ctx, "netbox-primary", core.ObjectDevices)
		if cursor != "2026-02-01T00:00:00Z" {
			t.Fatalf("expected overwritten cursor, got %q", cursor)
		}
	})

	t.Run("mapping round-trip", func(t *testing.T) {
		s := newStore(t)
		if _, ok, err := s.GetMapping(ctx, "netbox-primary", core.ObjectDevices, "42"); err != nil || ok {
			t.Fatalf("expected no mapping initially, got ok=%v err=%v", ok, err)
		}
		m := Mapping{SourceName: "netbox-primary", ObjectType: core.ObjectDevices, ExternalID: "42", KentikID: "1001", ContentHash: "abc"}
		if err := s.UpsertMapping(ctx, m); err != nil {
			t.Fatalf("UpsertMapping: %v", err)
		}
		got, ok, err := s.GetMapping(ctx, "netbox-primary", core.ObjectDevices, "42")
		if err != nil || !ok || got.KentikID != "1001" || got.ContentHash != "abc" {
			t.Fatalf("unexpected mapping: %+v ok=%v err=%v", got, ok, err)
		}

		m.ContentHash = "def"
		if err := s.UpsertMapping(ctx, m); err != nil {
			t.Fatalf("UpsertMapping overwrite: %v", err)
		}
		got, _, _ = s.GetMapping(ctx, "netbox-primary", core.ObjectDevices, "42")
		if got.ContentHash != "def" {
			t.Fatalf("expected updated content hash, got %q", got.ContentHash)
		}

		if err := s.DeleteMapping(ctx, "netbox-primary", core.ObjectDevices, "42"); err != nil {
			t.Fatalf("DeleteMapping: %v", err)
		}
		if _, ok, _ := s.GetMapping(ctx, "netbox-primary", core.ObjectDevices, "42"); ok {
			t.Fatal("expected mapping to be gone after delete")
		}
	})

	t.Run("list mappings scoped by source and object type", func(t *testing.T) {
		s := newStore(t)
		must := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		}
		must(s.UpsertMapping(ctx, Mapping{SourceName: "a", ObjectType: core.ObjectDevices, ExternalID: "1", KentikID: "k1"}))
		must(s.UpsertMapping(ctx, Mapping{SourceName: "a", ObjectType: core.ObjectDevices, ExternalID: "2", KentikID: "k2"}))
		must(s.UpsertMapping(ctx, Mapping{SourceName: "a", ObjectType: core.ObjectSites, ExternalID: "3", KentikID: "k3"}))
		must(s.UpsertMapping(ctx, Mapping{SourceName: "b", ObjectType: core.ObjectDevices, ExternalID: "4", KentikID: "k4"}))

		got, err := s.ListMappings(ctx, "a", core.ObjectDevices)
		if err != nil {
			t.Fatalf("ListMappings: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("expected 2 mappings for source a/devices, got %d: %+v", len(got), got)
		}
	})
}

func TestMemoryStore(t *testing.T) {
	runStoreTests(t, func(t *testing.T) Store { return NewMemoryStore() })
}

func TestSQLiteStore(t *testing.T) {
	runStoreTests(t, func(t *testing.T) Store {
		path := filepath.Join(t.TempDir(), "state.db")
		s, err := OpenSQLite(path)
		if err != nil {
			t.Fatalf("OpenSQLite: %v", err)
		}
		t.Cleanup(func() { s.Close() })
		return s
	})
}
