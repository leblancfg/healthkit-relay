# Installing the server

Three ways to run healthkit-relay. Each one is tested end to end in CI
([`integration.yml`](../.github/workflows/integration.yml)) by
[`scripts/smoke-test.sh`](../scripts/smoke-test.sh): pair like the app, upload a batch, check
it is stored, restart, check it is still there.

Whichever you pick, the server needs two things:

- a **token file**: the admin credential, at least 16 characters. Keep it private.
- a **public URL**: the address your iPhone uses to reach the server, put in setup codes
  (`HKR_PUBLIC_URL`), for instance `https://health.example.com` behind a reverse proxy, or
  `http://192.168.1.10:8080` on a home network.

Every flag can also be set as an `HKR_` environment variable; see
[server/README.md](../server/README.md#configuration). Once it runs, pair the app with
`healthkit-relay pair` (a QR code in the terminal) or the `/setup` page.

## Docker

Images for linux/amd64 and linux/arm64 are on `ghcr.io/leblancfg/healthkit-relay`: `latest`
and `X.Y.Z` for releases, `edge` for the main branch. The image is distroless, runs as a
non-root user and keeps its archive in `/data`.

With [`docker-compose.yml`](../docker-compose.yml), in a directory of its own:

```sh
curl -fsSLO https://raw.githubusercontent.com/leblancfg/healthkit-relay/main/docker-compose.yml
mkdir -p data
(umask 077; head -c 32 /dev/urandom | base64 > token)
printf 'HKR_USER=%s\nHKR_PUBLIC_URL=%s\n' "$(id -u):$(id -g)" http://192.168.1.10:8080 > .env
docker compose up -d
docker compose exec relay healthkit-relay pair
```

The container runs as you (`HKR_USER`), so `./data` and `./token` stay yours and back up with
the directory. To also get VictoriaMetrics and Grafana (on port 3000, admin/admin at first
login) with the samples forwarded, add
`HKR_FORWARD_URL=http://victoriametrics:8428/influx/write` to `.env` and start with
`docker compose --profile metrics up -d`.

Without Compose:

```sh
docker run -d --name healthkit-relay -p 8080:8080 --user "$(id -u):$(id -g)" \
  -e HKR_PUBLIC_URL=http://192.168.1.10:8080 \
  -v "$PWD/data:/data" -v "$PWD/token:/run/secrets/hkr_token:ro" \
  ghcr.io/leblancfg/healthkit-relay
```

## Binary and systemd

Download the archive for your platform from the
[releases](https://github.com/leblancfg/healthkit-relay/releases) (Linux, macOS and FreeBSD,
amd64 and arm64; `checksums.txt` lists their SHA-256), then on a systemd distribution, as
root:

```sh
sha256sum -c --ignore-missing checksums.txt
tar -xzf healthkit-relay_*_linux_amd64.tar.gz
install -m 755 healthkit-relay /usr/local/bin/
install -m 644 packaging/healthkit-relay.service /etc/systemd/system/
install -d -m 700 /etc/healthkit-relay
(umask 077; head -c 32 /dev/urandom | base64 > /etc/healthkit-relay/token)
echo HKR_PUBLIC_URL=https://health.example.com > /etc/healthkit-relay/env
systemctl daemon-reload
systemctl enable --now healthkit-relay
healthkit-relay pair -token-file /etc/healthkit-relay/token
```

The unit ([`packaging/healthkit-relay.service`](../packaging/healthkit-relay.service)) runs the
server as a dynamic user, sandboxed, with the archive in `/var/lib/healthkit-relay` and the
token passed as a systemd credential. It listens on `127.0.0.1:8080`, for a reverse proxy;
put `HKR_LISTEN=0.0.0.0:8080` in `/etc/healthkit-relay/env` to accept phones directly (then
pass `-listen 0.0.0.0:8080` to `pair` too, or `-server http://127.0.0.1:8080`).

## NixOS

The flake provides a package and a NixOS module:

```nix
{
  inputs.healthkit-relay.url = "github:leblancfg/healthkit-relay";

  outputs = { nixpkgs, healthkit-relay, ... }: {
    nixosConfigurations.myhost = nixpkgs.lib.nixosSystem {
      modules = [
        healthkit-relay.nixosModules.default
        {
          services.healthkit-relay = {
            enable = true;
            publicUrl = "https://health.example.com";
            # listen = "0.0.0.0:8080"; openFirewall = true;   # phones on the LAN, no proxy
            # tokenFile = "/run/secrets/healthkit-relay";      # default: generated on first start
            # forwardUrl = "http://127.0.0.1:8428/influx/write";
          };
        }
      ];
    };
  };
}
```

Then `sudo healthkit-relay-pair` prints the setup code. The archive is in
`/var/lib/healthkit-relay`. `nix flake check` runs the module in a VM with the smoke test.

## Building from source

```sh
make server-build                    # ./bin/healthkit-relay, needs Go
docker build -t healthkit-relay .    # the image
nix build                            # the Nix package
goreleaser release --snapshot --clean   # all release archives, in dist/
```
