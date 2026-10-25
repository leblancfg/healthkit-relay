# Docker & Compose

Docker runs the server in a *container*: a packaged program with everything it needs. Docker
Compose starts it from a `docker-compose.yml` file that holds its settings. This guide works
on any Linux or macOS machine with Docker; the [Quick start](../quickstart.md) follows the
same steps with more detail on each.

The image `ghcr.io/leblancfg/healthkit-relay` runs on `linux/amd64` and `linux/arm64`. It
holds the server and a health check, on a distroless base (a minimal image with only what
the program needs). It runs as a non-root user, `65532` unless you choose another, and keeps
its archive in `/data`.

| Tag | What |
|---|---|
| `latest` | the latest release |
| `X.Y.Z`, e.g. `0.7.0` | one release |
| `X.Y`, e.g. `0.7` | the latest patch release of that minor line |
| `X` | the latest release of that major line (from 1.0 on) |
| `edge`, `sha-<commit>` | the main branch, between releases |

To choose when you update, pin a version: `HKR_VERSION=0.7.0` in `.env` (see below).

The image sets `HKR_LISTEN=:8080` and `HKR_ARCHIVE_DIR=/data`, so you mount the archive at
`/data` and set the server address. Every other [flag](index.md#configuration) can be set
as an `HKR_` variable.

## Docker Compose

The repository's
[`docker-compose.yml`](https://github.com/leblancfg/healthkit-relay/blob/main/docker-compose.yml)
is the supported setup, and the one CI tests end to end. Replace `192.168.1.10` with the IP
address of the computer running Docker (`hostname -I` on Linux, `ipconfig getifaddr en0` on
macOS).

1. Make a folder for the server and go into it:

    ```sh
    mkdir -p ~/healthkit-relay && cd ~/healthkit-relay
    ```

2. Download the Compose file, make the archive folder `data` and write your settings to
   `.env`:

    ```sh
    curl -fsSLO https://raw.githubusercontent.com/leblancfg/healthkit-relay/main/docker-compose.yml
    mkdir -p data
    printf 'HKR_USER=%s\nHKR_PUBLIC_URL=%s\n' "$(id -u):$(id -g)" http://192.168.1.10:8080 > .env
    ```

    `cat .env` shows two lines, e.g. `HKR_USER=1000:1000` and
    `HKR_PUBLIC_URL=http://192.168.1.10:8080`.

3. Start the server:

    ```sh
    docker compose up -d
    ```

    The last line ends with `Started`. After a few seconds, `docker compose ps` shows the
    container as `healthy`: it answers `http://127.0.0.1:8080/healthz`.

4. Read the admin token, which the server prints once, on its first start:

    ```sh
    docker compose logs relay
    ```

    The log shows it, with the setup page's address:

    ```text
    relay-1  |     Admin token: BBvGW16otZlTAfIc7RbrNmMVd7PXBVU8skBFvHZhO_4
    relay-1  |     Setup page:  http://192.168.1.10:8080/setup
    ```

    Save the admin token in a password manager: it signs in to the setup page.

5. Pair your iPhone. Open `http://192.168.1.10:8080/setup` and sign in with the admin token,
   or print the QR code in the terminal:

    ```sh
    docker compose exec relay healthkit-relay pair
    ```

    Then follow [Pairing an iPhone](../pairing.md).

The other [commands](index.md#commands) run the same way, in the server's folder:

```sh
docker compose exec relay healthkit-relay devices                  # paired devices and read tokens
docker compose exec relay healthkit-relay revoke 3f9a1c2e
docker compose exec relay healthkit-relay token create -name grafana
docker compose exec relay healthkit-relay admin reset              # a new admin token
```

`admin reset` prints a new admin token, and the old one stops working at once.

The service is called `relay`. It reads these settings from `.env`:

| Variable | |
|---|---|
| `HKR_USER` | required: the `uid:gid` (user and group IDs) the server runs as. Use your own, so that `./data` stays yours and is backed up with the folder |
| `HKR_PUBLIC_URL` | required: the server address, put in setup codes, e.g. `http://192.168.1.10:8080` or `https://health.example.com` |
| `HKR_VERSION` | image tag, default `latest`; e.g. `HKR_VERSION=0.7.0` to pin |
| `HKR_PORT` | port on the host, default `8080` |
| `HKR_FORWARD_URL` | optional: forward numeric samples to a metrics database (below) |

`./data` is the archive. It holds your data and the server's state: `staging.db`,
`parquet/`, `admin.json`, `devices.json` and `codes.json`.

!!! note "The server address in a container"
    Inside a container, the server sees the container's own network, so it learns the
    server address from `HKR_PUBLIC_URL`. The Compose file requires it. Open the setup page
    at that same address, e.g. `http://192.168.1.10:8080/setup`.

## With VictoriaMetrics and Grafana

The same Compose file has a `metrics` profile. It adds VictoriaMetrics, a time-series
database, with a retention of 100 years, and Grafana, with VictoriaMetrics already set up as
its data source. Add the forward URL to `.env` and start with the profile:

```sh
echo HKR_FORWARD_URL=http://victoriametrics:8428/influx/write >> .env
docker compose --profile metrics up -d
```

Grafana is on `http://localhost:3000`, on this computer only. `GRAFANA_PORT` in `.env`
changes the port. The first login is `admin`, password `admin`: Grafana then asks for a new
password. To open Grafana from other computers, set that password first, then add
`GRAFANA_BIND=0.0.0.0` to `.env` and run `docker compose --profile metrics up -d` again. See [Grafana](../grafana.md) for queries. The long
retention matters: the first sync sends years of history, and a database with a short
retention drops most of it. VictoriaMetrics and Grafana keep their data in the Docker
volumes `victoriametrics` and `grafana`.

## Without Compose

```sh
mkdir -p data
docker run -d --name healthkit-relay --restart unless-stopped \
  -p 8080:8080 --user "$(id -u):$(id -g)" \
  -e HKR_PUBLIC_URL=http://192.168.1.10:8080 \
  -v "$PWD/data:/data" \
  ghcr.io/leblancfg/healthkit-relay:latest
docker logs healthkit-relay                         # the admin token
docker exec healthkit-relay healthkit-relay pair    # the QR code for the iPhone
```

## Updating

In the server's folder:

```sh
docker compose pull && docker compose up -d
```

With `HKR_VERSION` pinned, change it in `.env` first. The server upgrades the archive in
place when it starts; [back it up](../backups.md) before you update across minor versions,
e.g. from 0.6 to 0.7.

From 0.6 and earlier, where you made a `./token` file by hand and mounted it at
`/run/secrets/hkr_token`: on the first start of this version, the server imports the token's
hash into `data/admin.json` and logs a deprecation warning. The same token is now your admin
token and signs in to the setup page. Save it in a password manager, then delete `./token`
and remove its mount from your `docker-compose.yml`. Paired devices keep their tokens. An app
you set up by hand with the old token: pair it again from the setup page.

## Permissions

If the server cannot write `/data`, the folder `./data` belongs to another user than the one
the container runs as.

- With Compose, that user is `HKR_USER`: set it to the owner of `./data`, which
  `stat -c %u:%g data` prints on Linux (`stat -f %u:%g data` on macOS), e.g. `1000:1000`.
- With `docker run`, pass the same with `--user`. Without `--user` the image runs as
  `65532:65532`, which can write a Docker volume but not a folder you own.
