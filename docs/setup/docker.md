# Docker & Compose

The image `ghcr.io/leblancfg/healthkit-relay` is multi-arch (amd64 and arm64) and holds
only the static binary. Tags: `latest`, each release (`0.5.0`), and its minor line (`0.5`).
Pin a version in production and update deliberately.

## Docker Compose

```yaml title="compose.yaml"
services:
  healthkit-relay:
    image: ghcr.io/leblancfg/healthkit-relay:latest
    restart: unless-stopped
    ports:
      - "8080:8080"
    environment:
      HKR_LISTEN: 0.0.0.0:8080
      HKR_ARCHIVE_DIR: /data
      HKR_TOKEN_FILE: /run/secrets/hkr_token
      # HKR_PUBLIC_URL: https://health.example.com   # the address phones use
      # HKR_FORWARD_URL: http://victoriametrics:8428/influx/write
    volumes:
      - ./data:/data
    secrets:
      - hkr_token

secrets:
  hkr_token:
    file: ./token
```

```sh
head -c 32 /dev/urandom | base64 > token && chmod 600 token
docker compose up -d
docker compose logs -f healthkit-relay
```

!!! note "Pairing from a container"
    Inside a container the server cannot see the host's LAN or Tailscale addresses, so the
    setup page offers only the address you opened it with. Open `/setup` with the address
    the phone will use, or set `HKR_PUBLIC_URL`.

## With VictoriaMetrics and Grafana

A complete self-hosted stack: archive, metrics and dashboards on one machine.

```yaml title="compose.yaml"
services:
  healthkit-relay:
    image: ghcr.io/leblancfg/healthkit-relay:latest
    restart: unless-stopped
    ports: ["8080:8080"]
    environment:
      HKR_LISTEN: 0.0.0.0:8080
      HKR_ARCHIVE_DIR: /data
      HKR_TOKEN_FILE: /run/secrets/hkr_token
      HKR_FORWARD_URL: http://victoriametrics:8428/influx/write
    volumes: ["./data:/data"]
    secrets: [hkr_token]

  victoriametrics:
    image: victoriametrics/victoria-metrics:latest
    restart: unless-stopped
    command: ["-retentionPeriod=100y", "-storageDataPath=/storage"]
    volumes: ["./vm:/storage"]

  grafana:
    image: grafana/grafana:latest
    restart: unless-stopped
    ports: ["3000:3000"]
    volumes: ["./grafana:/var/lib/grafana"]

secrets:
  hkr_token:
    file: ./token
```

In Grafana add a **Prometheus** data source with URL `http://victoriametrics:8428`; see
[Grafana](../grafana.md) for queries. The long retention matters: the first sync sends years
of history, and a database with the default retention would drop most of it.

## Updating

```sh
docker compose pull && docker compose up -d
```

The data directory format is upgraded in place on start; [back it up](../backups.md) before
major version updates.

## Permissions

If the container cannot write `./data`, the directory belongs to another user than the one
the image runs as. Check with `docker compose logs`, then `chown` the directory to that user
ID or run the service with `user: "1000:1000"` matching the owner.
