# Linux binary + systemd

For any Linux server or VPS with systemd, amd64 or arm64. No Docker needed.

## Install the binary

Each [release](https://github.com/leblancfg/healthkit-relay/releases) has an archive per
platform, `healthkit-relay_X.Y.Z_<os>_<arch>.tar.gz` (Linux, macOS and FreeBSD; amd64 and
arm64), and a `checksums.txt` with their SHA-256. Each archive holds the binary, the systemd
unit `packaging/healthkit-relay.service`, the READMEs and the license. As root:

```sh
VERSION=0.6.0                       # the latest release
ARCH=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
curl -fLO "https://github.com/leblancfg/healthkit-relay/releases/download/v${VERSION}/healthkit-relay_${VERSION}_linux_${ARCH}.tar.gz"
curl -fLO "https://github.com/leblancfg/healthkit-relay/releases/download/v${VERSION}/checksums.txt"
sha256sum --check --ignore-missing checksums.txt
tar -xzf "healthkit-relay_${VERSION}_linux_${ARCH}.tar.gz"
install -m 755 healthkit-relay /usr/local/bin/
healthkit-relay -version
```

Or build from source with Go: `make server-build` puts it in `bin/healthkit-relay`, and the
unit is in the repository's `packaging/` directory.

## systemd service

Still as root, in the directory you unpacked the archive in:

```sh
install -m 644 packaging/healthkit-relay.service /etc/systemd/system/
install -d -m 700 /etc/healthkit-relay
(umask 077; head -c 32 /dev/urandom | base64 > /etc/healthkit-relay/token)
echo HKR_PUBLIC_URL=https://health.example.com > /etc/healthkit-relay/env
systemctl daemon-reload
systemctl enable --now healthkit-relay
journalctl -u healthkit-relay -f
```

The unit
([`packaging/healthkit-relay.service`](https://github.com/leblancfg/healthkit-relay/blob/main/packaging/healthkit-relay.service))
runs the server as a dynamic user, sandboxed, with:

- the archive (`staging.db`, `devices.json`, `parquet/`) in `/var/lib/healthkit-relay`, mode
  0700 (systemd keeps it in `/var/lib/private/healthkit-relay`, with a symlink);
- the token, `/etc/healthkit-relay/token`, handed to the service as a systemd credential, so
  the file stays owned by root with mode 600;
- its settings read from `/etc/healthkit-relay/env`, one `HKR_<FLAG>=value` per line (see
  [Configuration](index.md#configuration)), e.g.:

```sh title="/etc/healthkit-relay/env"
HKR_PUBLIC_URL=https://health.example.com
# HKR_LISTEN=0.0.0.0:8080                                  # phones on the LAN, no proxy
# HKR_FORWARD_URL=http://127.0.0.1:8428/influx/write       # metrics, see Grafana
```

It listens on `127.0.0.1:8080` by default, for a reverse proxy or `tailscale serve` (see
[Networking & HTTPS](../networking.md)). For LAN-only use, put `HKR_LISTEN=0.0.0.0:8080` in
the env file. After editing it, `systemctl restart healthkit-relay`.

To change something the env file cannot, such as the paths, use
`systemctl edit healthkit-relay` rather than editing the unit, so updates can replace it.

## Pairing from the terminal

The `pair` command talks to the running server and prints a QR code in the terminal. It
needs the token, so run it as root:

```sh
sudo healthkit-relay pair -token-file /etc/healthkit-relay/token
```

It connects to `127.0.0.1:8080`; if you changed the port in `HKR_LISTEN`, pass the same
`-listen`, or `-server http://127.0.0.1:<port>`. `devices` and `revoke` work the same way.

## Updating

Download the new archive as above, replace `/usr/local/bin/healthkit-relay` and the unit
(`install` both again), then `systemctl daemon-reload && systemctl restart healthkit-relay`.
