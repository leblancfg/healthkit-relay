# Linux binary + systemd

For any Linux server or VPS, amd64 or arm64. No Docker needed.

## Install the binary

Download the archive for your architecture from the
[releases page](https://github.com/leblancfg/healthkit-relay/releases) and check it:

```sh
VERSION=0.5.0                       # the latest release
ARCH=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
curl -LO "https://github.com/leblancfg/healthkit-relay/releases/download/v${VERSION}/healthkit-relay_${VERSION}_linux_${ARCH}.tar.gz"
curl -LO "https://github.com/leblancfg/healthkit-relay/releases/download/v${VERSION}/checksums.txt"
sha256sum --check --ignore-missing checksums.txt
tar xzf "healthkit-relay_${VERSION}_linux_${ARCH}.tar.gz" healthkit-relay
sudo install -m 755 healthkit-relay /usr/local/bin/
healthkit-relay -version
```

Or build from source with Go: `make server-build` puts it in `bin/healthkit-relay`.

## Token

```sh
sudo install -d -m 700 /etc/healthkit-relay
head -c 32 /dev/urandom | base64 | sudo tee /etc/healthkit-relay/token >/dev/null
sudo chmod 600 /etc/healthkit-relay/token
```

## systemd service

The unit runs as a dynamic user with a private data directory, and hands it the token
through `LoadCredential=`, so only the service can read it.

```ini title="/etc/systemd/system/healthkit-relay.service"
[Unit]
Description=Health Relay: Apple Health receiver and archive
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/healthkit-relay \
  -listen 127.0.0.1:8080 \
  -archive-dir /var/lib/healthkit-relay \
  -token-file ${CREDENTIALS_DIRECTORY}/token
# Environment=HKR_PUBLIC_URL=https://health.example.com
# Environment=HKR_FORWARD_URL=http://127.0.0.1:8428/influx/write
LoadCredential=token:/etc/healthkit-relay/token
DynamicUser=yes
StateDirectory=healthkit-relay
StateDirectoryMode=0700
Restart=on-failure

NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
RestrictNamespaces=yes
LockPersonality=yes
MemoryDenyWriteExecute=yes
SystemCallArchitectures=native

[Install]
WantedBy=multi-user.target
```

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now healthkit-relay
journalctl -u healthkit-relay -f
```

`-listen 127.0.0.1:8080` keeps it behind a reverse proxy or `tailscale serve` (see
[Networking & HTTPS](../networking.md)). For LAN-only use, listen on `0.0.0.0:8080` instead.

## Pairing from the terminal

The `pair` command talks to the running server and prints a QR code in the terminal:

```sh
sudo healthkit-relay pair -token-file /etc/healthkit-relay/token -public-url https://health.example.com
```

## Updating

Replace `/usr/local/bin/healthkit-relay` with the new release and
`sudo systemctl restart healthkit-relay`.
