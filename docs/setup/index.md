# Setup guides

The server is one static binary with no runtime dependencies, published as:

- a multi-arch **Docker image**, `ghcr.io/leblancfg/healthkit-relay` (`linux/amd64`,
  `linux/arm64`), with a Compose file;
- **release archives** for Linux, macOS and FreeBSD on amd64 and arm64, with a systemd unit,
  on the [releases page](https://github.com/leblancfg/healthkit-relay/releases);
- a **Nix flake** (`github:leblancfg/healthkit-relay`) with a package and a NixOS module.

Each of the Docker Compose, binary + systemd and NixOS setups is tested end to end in CI: the
server is installed as these guides say, then a smoke test pairs like the app, uploads a
batch, checks it is stored, restarts the server and checks it is still there.

| Where | Guide |
|---|---|
| Any machine with Docker | [Docker & Compose](docker.md) |
| A Linux server or VPS | [Linux binary + systemd](linux.md) |
| NixOS | [NixOS](nixos.md) |
| A Mac that stays on | [macOS (launchd)](macos.md) |
| A NAS | [Synology, Unraid, TrueNAS](nas.md) |
| A Raspberry Pi or other ARM board | [Raspberry Pi & ARM](raspberry-pi.md) |

## What every setup needs

- **A data directory** on a disk with room to grow and in your backups. A few years of
  Apple Watch data is typically a few gigabytes before compression.
- **A server token** in a file only the server can read:
  `head -c 32 /dev/urandom | base64 > token && chmod 600 token`.
  It is read from a file, never a command-line value, so it stays out of process listings.
- **A way for the phone to reach it**: see [Networking & HTTPS](../networking.md).

## Configuration

Every flag can be set from the environment: `HKR_` plus the flag name in upper case with
dashes as underscores (`-archive-dir` is `HKR_ARCHIVE_DIR`). Flags win over the environment.

| Flag | Default | |
|---|---|---|
| `-listen` | `127.0.0.1:8080` | address to listen on; `0.0.0.0:8080` to accept other machines (and in containers) |
| `-archive-dir` | required | data directory: `staging.db`, `parquet/`, `devices.json` |
| `-token-file` | required | file holding the server token (16+ characters) |
| `-public-url` | offered | the URL phones use, put in setup codes, e.g. `https://health.example.com` |
| `-setup-from` | `private` | who may open `/setup` and pair: `private`, `any` or `off` |
| `-forward-url` | | line-protocol write URL for metrics forwarding (see [Grafana](../grafana.md)) |
| `-forward-token-file` | | bearer token for `-forward-url` |
| `-measurement` | `healthkit` | line-protocol measurement name |
| `-max-body-bytes` | `33554432` | largest accepted request |
| `-flush-interval` | `1h` | how often staged records are written to Parquet |
| `-compact-at` | `03:30` | local time of the nightly compaction, `HH:MM` |
| `-group-read` | off | make the archive readable by the service's group (0750/0640) for backups or queries |
| `-version` | | print the version and exit |

## Commands

The same binary has subcommands, which take the same flags and `HKR_` variables as the
server:

- against the **running** server, authenticated with the token file: `pair` (a setup QR code
  in the terminal; `-valid 2h` for a longer-lived code), `devices` (list paired phones) and
  `revoke <id>`. They connect to the `-listen` address, or `-server <url>`;
- on the archive directory with the server **stopped**: `migrate` (convert the JSON Lines
  archive of versions before 0.5), `flush` and `compact` (run the hourly and nightly
  maintenance now).

The authoritative list is `healthkit-relay -help` and the
[server README](https://github.com/leblancfg/healthkit-relay/blob/main/server/README.md).
