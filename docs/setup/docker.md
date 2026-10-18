# Docker & Compose

The image `ghcr.io/leblancfg/healthkit-relay` is multi-arch (`linux/amd64` and `linux/arm64`)
and holds only the static server binary and a health check, on a distroless base. It runs as
a non-root user (65532 unless you choose another) and keeps its archive in `/data`.

| Tag | What |
|---|---|
| `latest` | the latest release |
| `X.Y.Z`, e.g. `0.6.0` | one release |
| `X.Y`, e.g. `0.6` | the latest patch release of that minor line |
| `X` | the latest release of that major line (from 1.0 on) |
| `edge`, `sha-<commit>` | the main branch, between releases |

Pin a version in production and update deliberately.

The image sets `HKR_LISTEN=:8080`, `HKR_ARCHIVE_DIR=/data` and
`HKR_TOKEN_FILE=/run/secrets/hkr_token`, so you only mount the data directory and the token
file there. Every other [flag](index.md#configuration) can be set as an `HKR_` variable.

## Docker Compose

The repository's
[`docker-compose.yml`](https://github.com/leblancfg/healthkit-relay/blob/main/docker-compose.yml)
is the supported setup, and the one CI tests end to end. In a directory of its own:

```sh
curl -fsSLO https://raw.githubusercontent.com/leblancfg/healthkit-relay/main/docker-compose.yml
mkdir -p data
(umask 077; head -c 32 /dev/urandom | base64 > token)
printf 'HKR_USER=%s\nHKR_PUBLIC_URL=%s\n' "$(id -u):$(id -g)" http://192.168.1.10:8080 > .env
docker compose up -d
docker compose exec relay healthkit-relay pair      # QR code for the iPhone app
```

The service is called `relay`. It reads these from `.env`:

| Variable | |
|---|---|
| `HKR_USER` | required: the `uid:gid` the server runs as. Use your own, so `./data` and `./token` stay yours and are backed up with the directory |
| `HKR_PUBLIC_URL` | required: the URL phones use to reach the server, put in setup codes, e.g. `http://192.168.1.10:8080` or `https://health.example.com` |
| `HKR_VERSION` | image tag, default `latest`; e.g. `HKR_VERSION=0.6.0` to pin |
| `HKR_PORT` | host port, default `8080` |
| `HKR_FORWARD_URL` | optional: forward numeric samples to a metrics database (below) |

`./data` holds the archive (`staging.db`, `devices.json`, `parquet/`); `./token` is mounted
read-only at `/run/secrets/hkr_token`. `docker compose ps` shows the container as `healthy`
once it answers `/healthz`.

!!! note "Pairing from a container"
    Inside a container the server cannot see the host's LAN or Tailscale addresses, which is
    why the Compose file requires `HKR_PUBLIC_URL`. If you use the `/setup` page instead of
    `pair`, open it with the address the phone will use.

## With VictoriaMetrics and Grafana

The same Compose file has a `metrics` profile that adds VictoriaMetrics (with a 100-year
retention) and Grafana, with the VictoriaMetrics data source already provisioned. Add the
forward URL to `.env` and start with the profile:

```sh
echo HKR_FORWARD_URL=http://victoriametrics:8428/influx/write >> .env
docker compose --profile metrics up -d
```

Grafana is on `http://localhost:3000` (`GRAFANA_PORT` changes the port; `admin`/`admin` at
the first login). See [Grafana](../grafana.md) for queries. The long retention matters: the
first sync sends years of history, and a database with the default retention would drop most
of it. VictoriaMetrics and Grafana keep their data in the named volumes `victoriametrics` and
`grafana`.

## Without Compose

```sh
mkdir -p data
(umask 077; head -c 32 /dev/urandom | base64 > token)
docker run -d --name healthkit-relay --restart unless-stopped \
  -p 8080:8080 --user "$(id -u):$(id -g)" \
  -e HKR_PUBLIC_URL=http://192.168.1.10:8080 \
  -v "$PWD/data:/data" -v "$PWD/token:/run/secrets/hkr_token:ro" \
  ghcr.io/leblancfg/healthkit-relay:0.6.0
docker exec healthkit-relay healthkit-relay pair
```

## Updating

```sh
docker compose pull && docker compose up -d
```

With `HKR_VERSION` pinned, change it in `.env` first. The data directory format is upgraded
in place on start; [back it up](../backups.md) before updating across minor versions.

## Permissions

If the server cannot write `/data` or read the token, those files belong to another user than
the one the container runs as. With Compose that is `HKR_USER`: set it to the owner of
`./data` and `./token` (`stat -c %u:%g data`). With `docker run`, pass the same with
`--user`; without it the image runs as `65532:65532`, which can write a named volume but not
a host directory you own.
