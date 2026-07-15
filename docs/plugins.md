# Adding a new source plugin

A source plugin fetches inventory from one system (NetBox, Nautobot,
Infoblox, ...) and normalizes it into `internal/core` types. Everything
downstream — the state store, the reconciliation engine, the Kentik
destination — only ever sees `core.Device`, `core.Site`, and `core.IPGroup`.
Adding a source means implementing one interface; nothing else in the
codebase should need to change.

## Steps

1. Create `internal/source/<name>/` implementing `source.Source`
   (`internal/source/plugin.go`):

   ```go
   type Source interface {
       Name() string
       Capabilities() []core.ObjectType
       SupportsIncremental() bool
       FetchDevices(ctx context.Context, since string) (core.FetchResult[core.Device], error)
       FetchSites(ctx context.Context, since string) (core.FetchResult[core.Site], error)
       FetchIPGroups(ctx context.Context, since string) (core.FetchResult[core.IPGroup], error)
       HealthCheck(ctx context.Context) error
   }
   ```

2. Register it via `init()`:

   ```go
   func init() { source.Register("nautobot", newFromRawConfig) }
   ```

3. Give your plugin its own typed `Config` struct for the `connection:` and
   `mapping:` blocks of its config entry, decoded from the raw
   `map[string]any` the shared config layer hands you (see
   `internal/source/netbox/config.go` for the pattern — round-trip through
   YAML marshal/unmarshal, then validate required fields).

4. Add a blank import in `cmd/kentik-sync-agent/main.go` so your plugin's
   `init()` runs:

   ```go
   _ "github.com/kentikethan/kentik_sync_agent/internal/source/nautobot"
   ```

5. Write fixture-based tests following `internal/source/netbox/netbox_test.go`:
   an `httptest.Server` serving canned JSON, asserting on the resulting
   `core.Device`/`core.Site`/`core.IPGroup` values. No real instance of the
   source system needed in CI.

If your source can't detect deletions (no change log / webhook), leave
`FetchResult.Deleted` nil — the engine's periodic full-reconciliation pass
(`sync.full_reconcile_interval` in config) will still catch drift by
diffing a full fetch against the state store. If your source can't do
incremental fetches at all, return `false` from `SupportsIncremental()` and
every run will be a full fetch.

Nautobot is a NetBox fork with a compatible-ish REST API; a Nautobot plugin
can reasonably wrap/embed the NetBox plugin's HTTP client internally and
just override the bits that differ. That's an implementation detail inside
`internal/source/nautobot`, invisible to the rest of the system.

## What we learned about Kentik's API while building the NetBox integration

The original design assumed Kentik's Device/Site objects would have some
generic label or custom-field slot to stash a `managed-by=kentik-sync-agent,
external-id=<id>` marker, so the agent could ask Kentik "what have I already
created?" and treat the local state store as just a cache. Reading Kentik's
actual generated Go client
([kentik/api-schema-public](https://github.com/kentik/api-schema-public))
showed that isn't available:

- `DeviceConcise` (the create/update request shape) has no labels or
  custom-field map. There's a separate Label service and an
  `UpdateDeviceLabels` RPC, but it attaches labels by a numeric ID
  resolved from the string-keyed Label service by name — not a reliable
  place to encode an external ID. (This RPC is put to legitimate use by the
  `device_labels` sync described below; it's just not usable as a
  source-of-truth marker.)
- `Site` has no labels, custom fields, or free-text field at all beyond its
  structured address/geo fields.
- `Populator` (the IP-group mechanism, via the Custom Dimension API) has no
  external-id field either, but its `Addr` field is literally the CIDR
  being matched — which already serves as a natural, unique-enough key
  within one Custom Dimension.

**Consequence:** `internal/state`'s local mapping table is the
*authoritative* record of what this agent created, not a performance
optimization on top of some Kentik-side discovery call. If you lose
`state.path` (e.g. an ephemeral container without a persistent volume), the
agent cannot recognize objects it created on a prior run and will attempt
to recreate them. Back up `state.path`, or mount a persistent volume — the
provided Helm chart defaults to one.

Other API shape details worth knowing if you touch
`internal/destination/kentik`:

- Kentik's Device API has real batch endpoints (`CreateDevices`,
  `UpdateDevices`, `DeleteDevices`) with partial-failure reporting via a
  `failed_devices` list; matching a batch response back to your input is
  done by device name (create) or Kentik ID (update/delete), since the API
  gives no other correlation key.
- Kentik's Site and Populator APIs have **no batch endpoints** — the
  `SiteApplier` and `PopulatorApplier` issue one RPC per item.
- Creating a Kentik device requires a `plan_id` (your Kentik billing plan)
  and a `device_subtype` string that must match one of the subtypes enabled
  on your account. Both are set from config (`kentik.default_plan_id`,
  `kentik.default_device_subtype`), since NetBox has no equivalent concept.

## Device label sync (`device_labels`)

`device_labels` is an opt-in `sync.objects` entry (alongside `sites`,
`devices`, `ip_groups`) that applies each device's NetBox tenant/role to it
in Kentik as labels, formatted `Netbox:Device:Tenant:<name>` and
`Netbox:Device:Role:<name>`. It requires `devices` to also be listed for the
same source (enforced by `Config.Validate`), since it resolves each item
against the device's already-synced Kentik ID rather than syncing anything
of its own.

Two things make this different from the `devices`/`sites`/`ip_groups`
appliers documented above, both stemming from how Kentik's label API
actually works (see `internal/destination/kentik/labels.go`):

- **Labels are attached by numeric ID, resolved from name.** `LabelConcise`
  (the type `UpdateDeviceLabels` accepts) carries only a label ID, never a
  name — so `LabelApplier` first calls `ListLabels`/`CreateLabel` to
  get-or-create an ID for each label name it wants to apply.
- **`UpdateDeviceLabels` replaces a device's entire label set, not just the
  ones this agent manages.** To avoid clobbering labels a person added by
  hand in the Kentik UI, `LabelApplier` reads a device's current labels via
  `GetDevice` first, keeps anything not prefixed `Netbox:`, and only
  replaces/removes labels under that prefix. This costs one extra `GetDevice`
  call per device per sync pass.

**IPAM subnet labels (tenant/VLAN/description) are not yet implemented.**
Kentik has no subnet/prefix-level object to attach a label to — only
devices carry labels — so exposing NetBox prefix data as Kentik labels would
mean matching each device's primary IP against the NetBox prefix that
contains it and folding that prefix's tenant/VLAN/description into the same
per-device label set `device_labels` already produces. That correlation
step (plus deciding the exact label names, e.g. `Netbox:Subnet:Tenant:<name>`)
is a natural follow-up once `device_labels` has some runtime mileage.

## Known gaps in the current implementation

- **NetBox delete detection**: NetBox's `/api/extras/object-changes/`
  change log could report deletions between incremental polls, but its
  response shape varies enough across NetBox versions that it wasn't wired
  up rather than guess at an untested schema. Deletes are still caught
  correctly via the periodic full-reconciliation pass
  (`full_reconcile_interval`, default 24h) — just not the instant an
  incremental poll runs.
- **Webhook-driven (push) sync**: deferred by design — v1 is
  interval-polling only. An `internal/source/netbox/webhook.go` receiver
  fed by NetBox Event Rules is a natural additive follow-up once the core
  engine has some runtime mileage.
- **Nautobot / Infoblox plugins**: not yet implemented; see "Steps" above.
- **Kentik destination tests**: covered by unit tests of the pure
  translation functions (`internal/destination/kentik/translate_test.go`)
  and the reconciliation engine's diff logic
  (`internal/sync/reconcile_test.go`), which is where the real
  create/update/delete decision-making lives. There isn't yet a `bufconn`-
  based fake gRPC server exercising `DeviceApplier`/`SiteApplier`/
  `PopulatorApplier` end-to-end against Kentik's actual generated server
  interfaces; that would be the next test investment if you're extending
  that package.
