# Synology, Unraid, TrueNAS

On a NAS, run the Docker image. The NAS is a good home for the data: it is on all the time,
has redundant disks and probably already has backups. The settings are the same everywhere:

| Setting | Value |
|---|---|
| Image | `ghcr.io/leblancfg/healthkit-relay:latest` (or a version tag, e.g. `0.6.0`) |
| Port | container `8080` → host `8080` (any free port) |
| Volume | a share folder, e.g. `/volume1/docker/healthkit-relay/data` → `/data` |
| Volume | the token file → `/run/secrets/hkr_token` (read-only) |
| `HKR_PUBLIC_URL` | the address phones use, e.g. `http://192.168.1.10:8080` or your HTTPS name |
| User | the owner of the data folder and token, as `uid:gid` (see below) |
| Restart policy | unless stopped |

The image already sets `HKR_LISTEN=:8080`, `HKR_ARCHIVE_DIR=/data` and
`HKR_TOKEN_FILE=/run/secrets/hkr_token`, so those need no variables.

Create the token once, from an SSH session on the NAS or any computer, and copy it into the
folder: `head -c 32 /dev/urandom | base64 > token`. Keep that folder out of SMB/NFS shares
other people can read.

The container runs as user `65532` unless told otherwise, which cannot write a share folder
you own. Either run it as the folder's owner (`id` over SSH shows your `uid:gid`; the Compose
file's `HKR_USER`, or the user setting of the container UI), or give the folder and token to
`65532`: `chown -R 65532:65532 data token`.

## Synology DSM 7 (Container Manager)

1. Install **Container Manager** from the Package Center.
2. In File Station create `docker/healthkit-relay/data` and put the `token` file in
   `docker/healthkit-relay/`.
3. Container Manager › **Project** › **Create**, path `docker/healthkit-relay`, and upload
   the repository's `docker-compose.yml` from [Docker & Compose](docker.md). Put a `.env`
   file next to it with `HKR_USER` and `HKR_PUBLIC_URL`, or replace those two in the file.
4. Open `http://<nas-ip>:8080/setup` to pair.

For HTTPS on a Synology, the built-in reverse proxy (Control Panel › Login Portal ›
Advanced › Reverse Proxy) can forward `https://health.example.com` to `localhost:8080`; or
install the Tailscale package (see [Networking & HTTPS](../networking.md)).

## Unraid

1. **Docker** tab › **Add Container**, Repository `ghcr.io/leblancfg/healthkit-relay:latest`,
   Network type `bridge`.
2. Add a **Port** `8080` → `8080`, a **Path** `/mnt/user/appdata/healthkit-relay/data` →
   `/data`, a **Path** `/mnt/user/appdata/healthkit-relay/token` → `/run/secrets/hkr_token`
   (read-only), and the `HKR_PUBLIC_URL` **Variable**. Unraid's appdata belongs to
   `99:100` (`nobody:users`): add `--user 99:100` under **Extra Parameters** (advanced view).
3. Apply. The `appdata` share is covered by the usual Appdata Backup plugin.

## TrueNAS SCALE

1. Create a dataset, e.g. `tank/apps/healthkit-relay`, and put the `token` file in it.
2. **Apps** › **Discover Apps** › **Custom App** (or *Install via YAML* on recent versions,
   with the Compose file from [Docker & Compose](docker.md)).
3. Image `ghcr.io/leblancfg/healthkit-relay`, tag `latest` or a version; `HKR_PUBLIC_URL`;
   port `8080`; host-path storage `tank/apps/healthkit-relay/data` → `/data` and the token
   file → `/run/secrets/hkr_token`; the user and group IDs that own the dataset.
4. Snapshot the dataset on a schedule: that is your [backup](../backups.md) on the NAS.

If the container cannot write the data folder, give the folder to the user the container
runs as (see [Docker & Compose](docker.md#permissions)).
