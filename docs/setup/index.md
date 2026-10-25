---
description: >-
  Pick a setup guide for the Health Relay server: Docker and Compose, Linux with systemd, NixOS, macOS, Synology, Unraid, TrueNAS or Raspberry Pi.
---

# Setup guides

The server is the `healthkit-relay` program. It runs on a computer you keep on and receives
your iPhone's Health data. Pick the guide for that computer and follow it from top to
bottom. New to these words? The [glossary](../glossary.md) explains *server address*,
*setup page*, *admin token* and the rest, each with an example.

| Your computer | Guide |
|---|---|
| Any Linux or macOS machine with Docker (the easiest start) | [Docker & Compose](docker.md) |
| A Linux server or a VPS (virtual private server) | [Linux binary + systemd](linux.md) |
| A NixOS machine | [NixOS](nixos.md) |
| A Mac that stays on | [macOS (launchd)](macos.md) |
| A NAS: Synology, Unraid or TrueNAS | [Synology, Unraid, TrueNAS](nas.md) |
| A Raspberry Pi or another 64-bit ARM board | [Raspberry Pi & ARM](raspberry-pi.md) |

The server is one self-contained program, published as:

- a **Docker image**, `ghcr.io/leblancfg/healthkit-relay`, for `linux/amd64` and
  `linux/arm64`, with a Compose file;
- **release archives** for Linux, macOS and FreeBSD on amd64 and arm64, with a systemd unit,
  on the [releases page](https://github.com/leblancfg/healthkit-relay/releases);
- a **Nix flake**, `github:leblancfg/healthkit-relay`, with a package and a NixOS module.

The Docker Compose, binary + systemd and NixOS setups are tested end to end in CI
(continuous integration, the tests that run on every change): the server is installed as
these guides say, then a test pairs like the app, uploads a batch, checks that it is stored,
restarts the server and checks that the batch is still there.

## What every setup needs

- **An archive**: the folder where the server keeps your data. Put it on a disk with room to
  grow, and include it in your [backups](../backups.md). Each guide names it, e.g. `./data`
  with Docker Compose.
- **The admin token**: the password of your server. The server makes it on its first start
  and prints it once, in its log. Save it in a password manager: it signs in to the setup
  page. The archive keeps only its SHA-256 hash, in `admin.json`, and
  `healthkit-relay admin reset` makes a new one.
- **A server address**: the address your iPhone uses to reach the server, e.g.
  `http://192.168.1.10:8080` on your home Wi-Fi. [Networking & HTTPS](../networking.md)
  shows how to reach it from anywhere.

## Check your setup

Each guide ends with the server running. These steps check it, whichever guide you
followed.

1. On the computer running the server, ask it whether it answers:

    ```sh
    curl http://127.0.0.1:8080/healthz
    ```

    It prints `ok`.

2. Read the admin token in the server's log:

    | Setup | Command |
    |---|---|
    | Docker Compose | `docker compose logs relay` |
    | Docker without Compose | `docker logs healthkit-relay` |
    | systemd, NixOS | `sudo journalctl -u healthkit-relay` |
    | macOS | `cat ~/Library/Logs/healthkit-relay.log` |
    | NAS | the container's log in the NAS's web interface |

    The log shows the admin token and the setup page's address:

    ```text
        Admin token: BBvGW16otZlTAfIc7RbrNmMVd7PXBVU8skBFvHZhO_4
        Setup page:  http://192.168.1.10:8080/setup
    ```

3. Open the setup page in a browser on a computer on the same network, e.g.
   `http://192.168.1.10:8080/setup`. The page shows **Sign in** and an **Admin token**
   field.

4. Pair your iPhone: follow [Pairing an iPhone](../pairing.md).

## Configuration

Every flag has an environment variable: `HKR_` plus the flag name in upper case, with dashes
as underscores. For example, `-archive-dir /data` and `HKR_ARCHIVE_DIR=/data` do the same.
When both are set, the flag wins.

| Flag | Default | |
|---|---|---|
| `-listen` | `127.0.0.1:8080` | address and port to listen on. `127.0.0.1:8080` answers this computer only (for a reverse proxy or Tailscale); `0.0.0.0:8080` also answers other machines on your network. The Docker image sets `:8080` |
| `-archive-dir` | required | the archive: `staging.db`, `parquet/`, `admin.json`, `devices.json`, `codes.json` |
| `-public-url` | | the server address, put in setup codes, e.g. `http://192.168.1.10:8080` or `https://health.example.com`. Unset, the setup page offers the addresses it finds |
| `-setup-from` | `private` | who may open the setup page and pair: `private` (this computer, your home network, Tailscale), `any` or `off` |
| `-forward-url` | | line-protocol write URL to forward numeric samples to a metrics database (see [Grafana](../grafana.md)) |
| `-forward-token-file` | | file holding a bearer token for `-forward-url` |
| `-measurement` | `healthkit` | line-protocol measurement name |
| `-query-duckdb` | (off) | the `duckdb` program, which turns on the [query API](../query-api.md); the `-query-...` flags are listed there |
| `-max-body-bytes` | `33554432` | largest request the server accepts, in bytes (32 MiB) |
| `-flush-interval` | `1h` | how often staged records are written to Parquet |
| `-compact-at` | `03:30` | local time of the nightly compaction, `HH:MM` |
| `-group-read` | off | make the archive readable by the service's group (0750/0640), for backups or queries |
| `-token-file` | | deprecated: the server token file of earlier versions, imported into `admin.json` at start; delete the file once imported |
| `-version` | | print the version and exit |

## Commands

The same program has commands for pairing and tokens. They take the same flags and `HKR_`
variables as the server.

These change the files in the archive (`admin.json`, `devices.json`, `codes.json`), with the
server running or stopped. The server rereads the files when they change, so a command takes
effect at once.

| Command | What it does |
|---|---|
| `pair` | prints a setup code as a QR code in the terminal, valid 15 minutes; `-valid 2h` makes it last longer, up to `24h` |
| `devices` | lists paired devices and read tokens, with their IDs |
| `revoke <id>` | revokes a paired device or a read token, e.g. `revoke 3f9a1c2e` |
| `token create -name <name>` | prints a new read token for the [query API](../query-api.md), e.g. `-name grafana` |
| `admin reset` | prints a new admin token; the old one stops working at once |

Run them as the server's user or as root, for example:

```sh
docker compose exec relay healthkit-relay pair                  # Docker Compose
sudo healthkit-relay-ctl pair                                   # NixOS
sudo healthkit-relay pair -archive-dir /var/lib/healthkit-relay \
  -public-url http://192.168.1.10:8080                          # systemd
```

These work on the archive with the server **stopped**:

| Command | What it does |
|---|---|
| `migrate` | converts the JSON Lines archive of versions before 0.5 |
| `flush` | writes staged records to Parquet now (the hourly job) |
| `compact` | merges each partition's Parquet files now (the nightly job) |

`healthkit-relay -help` lists every flag, and the
[server README](https://github.com/leblancfg/healthkit-relay/blob/main/server/README.md)
describes each command.
