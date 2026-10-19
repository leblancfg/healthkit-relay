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
echo HKR_PUBLIC_URL=https://health.example.com > /etc/healthkit-relay/env
systemctl daemon-reload
systemctl enable --now healthkit-relay
journalctl -u healthkit-relay -f   # the admin token and the setup link, printed once
```

On first start the server generates the admin token and prints it once, with the setup link,
to the journal. Save it in a password manager: it signs in to `/setup`.

The unit
([`packaging/healthkit-relay.service`](https://github.com/leblancfg/healthkit-relay/blob/main/packaging/healthkit-relay.service))
runs the server as a dynamic user, sandboxed, with:

- the archive and its state (`staging.db`, `parquet/`, `admin.json`, `devices.json`,
  `codes.json`) in `/var/lib/healthkit-relay`, mode 0700 (systemd keeps it in
  `/var/lib/private/healthkit-relay`, with a symlink);
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

## Commands

`pair` prints a setup QR code in the terminal. It works on the state files in
`/var/lib/healthkit-relay` while the server runs, so run it as root with that directory:

```sh
sudo healthkit-relay pair -archive-dir /var/lib/healthkit-relay -public-url https://health.example.com
```

The commands do not read `/etc/healthkit-relay/env`: pass `-public-url` (or set
`HKR_PUBLIC_URL`) for the address in the code when the server listens on loopback only.
`devices`, `revoke <id>`, `token create -name grafana` and `admin reset` (a new admin token,
printed once; the old one stops working at once) take the same `-archive-dir`. Files they
write keep the owner of the archive directory.

## Updating

Download the new archive as above, replace `/usr/local/bin/healthkit-relay` and the unit
(`install` both again), then `systemctl daemon-reload && systemctl restart healthkit-relay`.

From 0.6 and earlier, with an `/etc/healthkit-relay/token`: before the restart, uncomment the
two lines marked *Earlier versions* in the new unit (`LoadCredential=` and
`Environment=HKR_TOKEN_FILE=`), so the server imports the token's hash into `admin.json` and
the token keeps working as the admin token. Once it has started, delete the token file and
those two lines, and restart. Without them the server generates a new admin token and prints
it to the journal.
