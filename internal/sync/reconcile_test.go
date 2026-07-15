package sync

import (
	"context"
	"testing"

	"github.com/kentikethan/kentik_sync_agent/internal/core"
	"github.com/kentikethan/kentik_sync_agent/internal/state"
)

func externalIDOfDevice(d core.Device) string { return d.ExternalID }

func TestDiff_NewItemsAreCreated(t *testing.T) {
	store := state.NewMemoryStore()
	ctx := context.Background()

	fetched := core.FetchResult[core.Device]{Items: []core.Device{{ExternalID: "1", Name: "dev-1"}}}
	p, err := diff(ctx, store, "netbox-primary", core.ObjectDevices, fetched, externalIDOfDevice, true)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(p.toCreate) != 1 || len(p.toUpdate) != 0 || len(p.toDelete) != 0 {
		t.Fatalf("expected 1 create, 0 update, 0 delete; got create=%d update=%d delete=%d", len(p.toCreate), len(p.toUpdate), len(p.toDelete))
	}
}

func TestDiff_UnchangedItemsAreSkipped(t *testing.T) {
	store := state.NewMemoryStore()
	ctx := context.Background()

	d := core.Device{ExternalID: "1", Name: "dev-1"}
	must(t, store.UpsertMapping(ctx, mappingFor("netbox-primary", core.ObjectDevices, d.ExternalID, "kentik-1", d)))

	fetched := core.FetchResult[core.Device]{Items: []core.Device{d}}
	p, err := diff(ctx, store, "netbox-primary", core.ObjectDevices, fetched, externalIDOfDevice, true)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(p.toCreate) != 0 || len(p.toUpdate) != 0 {
		t.Fatalf("expected no create/update for an unchanged item, got create=%d update=%d", len(p.toCreate), len(p.toUpdate))
	}
}

func TestDiff_ChangedItemsAreUpdated(t *testing.T) {
	store := state.NewMemoryStore()
	ctx := context.Background()

	original := core.Device{ExternalID: "1", Name: "dev-1"}
	must(t, store.UpsertMapping(ctx, mappingFor("netbox-primary", core.ObjectDevices, original.ExternalID, "kentik-1", original)))

	changed := core.Device{ExternalID: "1", Name: "dev-1-renamed"}
	fetched := core.FetchResult[core.Device]{Items: []core.Device{changed}}
	p, err := diff(ctx, store, "netbox-primary", core.ObjectDevices, fetched, externalIDOfDevice, true)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(p.toUpdate) != 1 {
		t.Fatalf("expected 1 update for a changed item, got %d", len(p.toUpdate))
	}
	if p.updateKentikID["1"] != "kentik-1" {
		t.Fatalf("expected update to carry forward the known Kentik id, got %q", p.updateKentikID["1"])
	}
}

func TestDiff_FullRunDeletesMissingItems(t *testing.T) {
	store := state.NewMemoryStore()
	ctx := context.Background()

	must(t, store.UpsertMapping(ctx, mappingFor("netbox-primary", core.ObjectDevices, "1", "kentik-1", core.Device{ExternalID: "1"})))
	must(t, store.UpsertMapping(ctx, mappingFor("netbox-primary", core.ObjectDevices, "2", "kentik-2", core.Device{ExternalID: "2"})))

	// Only device "1" comes back on a full fetch; "2" is gone.
	fetched := core.FetchResult[core.Device]{Items: []core.Device{{ExternalID: "1"}}}
	p, err := diff(ctx, store, "netbox-primary", core.ObjectDevices, fetched, externalIDOfDevice, true)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(p.toDelete) != 1 || p.toDelete["2"] != "kentik-2" {
		t.Fatalf("expected device 2 to be deleted, got %+v", p.toDelete)
	}
}

func TestDiff_IncrementalRunDoesNotDeleteUnmentionedItems(t *testing.T) {
	store := state.NewMemoryStore()
	ctx := context.Background()

	must(t, store.UpsertMapping(ctx, mappingFor("netbox-primary", core.ObjectDevices, "1", "kentik-1", core.Device{ExternalID: "1"})))
	must(t, store.UpsertMapping(ctx, mappingFor("netbox-primary", core.ObjectDevices, "2", "kentik-2", core.Device{ExternalID: "2"})))

	// An incremental fetch only reports what changed — device "2" simply
	// wasn't touched, which says nothing about whether it still exists.
	fetched := core.FetchResult[core.Device]{Items: []core.Device{{ExternalID: "1", Name: "renamed"}}}
	p, err := diff(ctx, store, "netbox-primary", core.ObjectDevices, fetched, externalIDOfDevice, false)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(p.toDelete) != 0 {
		t.Fatalf("expected no deletes on an incremental run, got %+v", p.toDelete)
	}
}

func TestDiff_ExplicitDeleteAppliesEvenOnIncrementalRun(t *testing.T) {
	store := state.NewMemoryStore()
	ctx := context.Background()

	must(t, store.UpsertMapping(ctx, mappingFor("netbox-primary", core.ObjectDevices, "1", "kentik-1", core.Device{ExternalID: "1"})))

	fetched := core.FetchResult[core.Device]{Deleted: []string{"1"}}
	p, err := diff(ctx, store, "netbox-primary", core.ObjectDevices, fetched, externalIDOfDevice, false)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(p.toDelete) != 1 || p.toDelete["1"] != "kentik-1" {
		t.Fatalf("expected explicit delete to be honored, got %+v", p.toDelete)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
