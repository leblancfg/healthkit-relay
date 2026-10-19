# Synology, Unraid, TrueNAS

On a NAS (network-attached storage), run the server's Docker image from the NAS's container
app. A NAS makes a good home for the archive: it is on all the time, has redundant disks and
often has backups already.

## Settings

The settings are the same on every NAS. Replace `192.168.1.10` with the NAS's IP address.

| Setting | Value |
|---|---|
| Image | `ghcr.io/leblancfg/healthkit-relay:latest`, or a version tag, e.g. `0.6.0` |
| Port | container `8080` → host `8080` (or another free port) |
| Folder | a shared folder for the archive, e.g. `/volume1/docker/healthkit-relay/data` → `/data` |
| `HKR_PUBLIC_URL` | the server address, e.g. `http://192.168.1.10:8080`, or your HTTPS address |
| User | the owner of the archive folder, as `uid:gid` (see below) |
| Restart policy | unless stopped |

The image already sets `HKR_LISTEN=:8080` and `HKR_ARCHIVE_DIR=/data`.

**The user.** The container runs as user `65532` unless you set another, and that user
cannot write a folder you own. Run it as the folder's owner: over SSH, `id` prints your
IDs, e.g. `uid=1026(you) gid=100(users)` gives `1026:100`. Or give the folder to `65532`:
`chown -R 65532:65532 /volume1/docker/healthkit-relay/data`.

**The admin token.** On its first start the server makes the admin token and prints it once,
in the container's log (the log view of the NAS's container app, or `docker logs` over SSH):

```text
    Admin token: BBvGW16otZlTAfIc7RbrNmMVd7PXBVU8skBFvHZhO_4
    Setup page:  http://192.168.1.10:8080/setup
```

Save it in a password manager: it signs in to the setup page. The archive holds its hash
(`admin.json`) and the hashes of the paired devices' tokens (`devices.json`): share the
archive folder with nobody else over SMB or NFS.

The NAS guides below use this Compose file. Write your user and server address in it:

```yaml title="docker-compose.yml"
services:
  relay:
    image: ghcr.io/leblancfg/healthkit-relay:latest
    restart: unless-stopped
    user: "1026:100"
    ports:
      - 8080:8080
    environment:
      HKR_PUBLIC_URL: http://192.168.1.10:8080
    volumes:
      - ./data:/data
```

## Synology DSM 7 (Container Manager)

1. In **Package Center**, install **Container Manager**.
2. In **File Station**, create the folder `docker/healthkit-relay/data`.
3. In **Container Manager**, open **Project** › **Create**. Set the path to
   `docker/healthkit-relay`, and create or upload the `docker-compose.yml` above. Finish the
   wizard. The project starts.
4. Open the project's log and find the admin token.
5. Open `http://192.168.1.10:8080/setup` and sign in with the admin token. Then follow
   [Pairing an iPhone](../pairing.md).

For HTTPS on a Synology, the built-in reverse proxy (Control Panel › Login Portal ›
Advanced › Reverse Proxy) can forward `https://health.example.com` to `localhost:8080`. Or
install the Tailscale package (see [Networking & HTTPS](../networking.md)).

## Unraid

1. In the **Docker** tab, click **Add Container**. Set **Repository** to
   `ghcr.io/leblancfg/healthkit-relay:latest` and **Network Type** to `bridge`.
2. Add a **Port** `8080` → `8080`, a **Path** `/mnt/user/appdata/healthkit-relay/data` →
   `/data`, and a **Variable** `HKR_PUBLIC_URL` with the server address. Unraid's
   `appdata` share belongs to `99:100` (`nobody:users`): switch to the advanced view and add
   `--user 99:100` under **Extra Parameters**.
3. Click **Apply**. The container starts.
4. Open the container's log and find the admin token.
5. Open `http://192.168.1.10:8080/setup` and sign in with the admin token. Then follow
   [Pairing an iPhone](../pairing.md).

The Appdata Backup plugin backs up the `appdata` share, archive included.

## TrueNAS SCALE

1. Create a dataset for the archive, e.g. `tank/apps/healthkit-relay`.
2. Open **Apps** › **Discover Apps** › **Custom App**. On recent versions you can use
   **Install via YAML** instead, with the Compose file above and the full path of the
   dataset in place of `./data`.
3. Set the image `ghcr.io/leblancfg/healthkit-relay` with the tag `latest` or a version,
   the variable `HKR_PUBLIC_URL`, the port `8080`, the host path
   `/mnt/tank/apps/healthkit-relay` → `/data`, and the user and group IDs that own the
   dataset.
4. Open the app's logs and find the admin token.
5. Open `http://192.168.1.10:8080/setup` and sign in with the admin token. Then follow
   [Pairing an iPhone](../pairing.md).
6. Snapshot the dataset on a schedule: that is your [backup](../backups.md) on the NAS.

If the container cannot write the archive folder, give the folder to the user the container
runs as (see [Docker & Compose](docker.md#permissions)).
