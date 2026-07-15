# kentik-sync-agent

A self-hosted agent that periodically syncs network inventory — devices,
sites, and IP address groupings — from source-of-truth systems (NetBox
first; Nautobot, Infoblox, and others planned) into
[Kentik](https://www.kentik.com/) via Kentik's public gRPC API
([kentik/api-schema-public](https://github.com/kentik/api-schema-public)).

You run it in your own environment. You decide what syncs, from where, and
how often, via one YAML config file.

## Status

NetBox → Kentik (devices, sites, IP-prefix-based grouping) is implemented
end to end. See [docs/plugins.md](docs/plugins.md) for what's built, what's
deliberately deferred, and how to add another source.

## Quickstart

1. **Build:**

   ```sh
   go build -o kentik-sync-agent ./cmd/kentik-sync-agent
   ```

2. **Configure.** Copy [config/example.yaml](config/example.yaml) and fill
   in your Kentik and NetBox details. At minimum you need:
   - A Kentik API token (Kentik > Profile > Authentication).
   - Your Kentik billing plan ID (`kentik.default_plan_id` — Admin > Plans).
   - An existing Kentik Custom Dimension to hold IP-group Populators
     (`kentik.custom_dimension_id` — create one under Admin > Custom
     Dimensions before first sync).
   - A NetBox URL and read-only API token.

   Secrets are referenced as `${ENV_VAR}`, never written inline:

   ```sh
   export KENTIK_EMAIL=you@example.com
   export KENTIK_API_TOKEN=...
   export NETBOX_TOKEN=...
   ```

3. **Dry-run it** against your real NetBox instance before touching Kentik:

   ```sh
   ./kentik-sync-agent run --config myconfig.yaml --once --dry-run
   ```

   This computes and logs exactly what would be created/updated/deleted,
   without calling Kentik or writing any local state.

4. **Run it for real, once:**

   ```sh
   ./kentik-sync-agent run --config myconfig.yaml --once
   ```

5. **Run it as a service** on the schedule your config defines:

   ```sh
   ./kentik-sync-agent run --config myconfig.yaml
   ```

   Or use the provided [Dockerfile](deploy/docker/Dockerfile), [systemd
   unit](deploy/systemd/kentik-sync-agent.service), or [Helm
   chart](deploy/helm/kentik-sync-agent/).

## How it works

- **Sources** (`internal/source/<name>`) fetch inventory from a system like
  NetBox and normalize it into `internal/core` types (`Device`, `Site`,
  `IPGroup`). Each configured source in `sources:` runs on its own
  `sync.interval`.
- **The sync engine** (`internal/sync`) diffs what a source just fetched
  against a local state store to decide create/update/delete, then applies
  that to Kentik in dependency order: sites and devices and IP groups are
  created/updated (sites before devices, so a device's `site_id` always
  resolves), then deleted in reverse order.
- **The destination** (`internal/destination/kentik`) wraps Kentik's
  generated gRPC clients for the Device, Site, and Custom
  Dimension/Populator APIs.
- **Local state** (`internal/state`, SQLite by default) is the
  authoritative record of which Kentik object corresponds to which source
  object — Kentik's Device/Site/Populator APIs have no custom-field slot to
  store that mapping themselves. Back up `state.path`, or mount a
  persistent volume in Docker/Kubernetes.

See [docs/plugins.md](docs/plugins.md) for the full architecture writeup,
including exactly what was learned about Kentik's API in the process of
building this (required fields, batch vs. non-batch endpoints, and the
label/custom-field limitation above).

## Observability

- Structured logs (JSON by default) to stdout.
- Prometheus metrics on `observability.metrics_addr` (default `:9090`).
- `/healthz` and `/readyz` on `observability.health_addr` (default `:8080`).
- `kentik-sync-agent healthcheck` for use in a Docker `HEALTHCHECK` or
  systemd watchdog.

## Testing

```sh
go build ./...
go vet ./...
go test ./...
```

## License

GPLv3 — see [LICENSE](LICENSE).
