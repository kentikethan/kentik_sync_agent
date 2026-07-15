# Kentik Sync Agent

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

There are no published binaries or container images yet, so you need the
source locally for any of the options below (building from source, Docker,
or Helm).

1. **Clone and build:**

   ```sh
   git clone https://github.com/kentikethan/kentik_sync_agent.git
   cd kentik_sync_agent
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

   For long-running use, pick one of the options below instead of running
   the binary directly in a terminal.

### Running as a service

**systemd** ([unit file](deploy/systemd/kentik-sync-agent.service)):

```sh
sudo useradd --system --no-create-home --shell /usr/sbin/nologin kentik-sync-agent
sudo cp kentik-sync-agent /usr/local/bin/kentik-sync-agent
sudo mkdir -p /etc/kentik-sync-agent /var/lib/kentik-sync-agent
sudo cp myconfig.yaml /etc/kentik-sync-agent/config.yaml
sudo chown -R kentik-sync-agent:kentik-sync-agent /var/lib/kentik-sync-agent

# Secrets referenced as ${VAR} in config.yaml go here as KEY=VALUE lines:
sudo tee /etc/kentik-sync-agent/env >/dev/null <<'EOF'
KENTIK_EMAIL=you@example.com
KENTIK_API_TOKEN=...
NETBOX_TOKEN=...
EOF
sudo chmod 600 /etc/kentik-sync-agent/env

sudo cp deploy/systemd/kentik-sync-agent.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now kentik-sync-agent
```

**Docker** ([Dockerfile](deploy/docker/Dockerfile)):

```sh
docker build -t kentik-sync-agent -f deploy/docker/Dockerfile .
docker run -d \
  --name kentik-sync-agent \
  -v "$(pwd)/myconfig.yaml:/etc/kentik-sync-agent/config.yaml:ro" \
  -v kentik-sync-agent-state:/var/lib/kentik-sync-agent \
  -e KENTIK_EMAIL=you@example.com \
  -e KENTIK_API_TOKEN=... \
  -e NETBOX_TOKEN=... \
  -p 8080:8080 -p 9090:9090 \
  kentik-sync-agent
```

**Helm** ([chart](deploy/helm/kentik-sync-agent/)) — for Kubernetes. The
chart isn't published to a registry, so build and push the image yourself
first (or point `image.repository`/`image.tag` at wherever you host it):

```sh
docker build -t <your-registry>/kentik-sync-agent:0.1.0 -f deploy/docker/Dockerfile .
docker push <your-registry>/kentik-sync-agent:0.1.0

helm install kentik-sync-agent deploy/helm/kentik-sync-agent \
  --set image.repository=<your-registry>/kentik-sync-agent \
  --set image.tag=0.1.0 \
  --set-string secretEnv.KENTIK_EMAIL=you@example.com \
  --set-string secretEnv.KENTIK_API_TOKEN=... \
  --set-string secretEnv.NETBOX_TOKEN=...
```

  The chart renders `secretEnv` into its own Secret (mounted as env vars)
  and `config` into a ConfigMap that becomes `config.yaml` — see
  [values.yaml](deploy/helm/kentik-sync-agent/values.yaml) for the full set
  of knobs, in particular the `sources:` list, which is empty by default and
  needs your NetBox source added.

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
