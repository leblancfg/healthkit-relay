# Backups

Your iPhone keeps Apple Health, and iCloud may too. The server's **archive**, the folder where
it keeps your data, is the copy in a form you control: back it up like any important folder.

## What to back up

Back up the whole archive directory (`-archive-dir`). Where it is depends on the setup:

| Setup | Archive directory |
|---|---|
| Docker Compose | `data`, next to `docker-compose.yml` |
| Linux binary + systemd | `/var/lib/private/healthkit-relay` (`/var/lib/healthkit-relay` is a link to it: back up the real folder) |
| NixOS | `/var/lib/healthkit-relay` |
| macOS | `~/Library/Application Support/healthkit-relay/data` |
| NAS | the share folder mapped to `/data` |

It holds the Parquet files (your data), the staging database (`staging.db`, what arrived
since the last hourly flush) and the state files: `admin.json` (the admin token's
fingerprint), `devices.json` (paired devices and read tokens) and `codes.json` (setup codes
not used yet). A backup without `admin.json` still restores everything else; then
`healthkit-relay admin reset` gives you a new admin token.

Everything the server forwards to a metrics database (VictoriaMetrics, InfluxDB) is also in
the archive, so the archive is the one backup your health data needs.

## Consistent copies

The server writes each Parquet file once: flush, nightly compaction and erase write a new
file and rename it into place, so every copied Parquet file is complete. `staging.db`
(SQLite) changes during uploads, and compaction replaces files. For a copy where all files
match, do one of these:

- Stop the server for the copy (`sudo systemctl stop healthkit-relay`, or
  `docker compose stop relay`). The app retries later; it keeps everything the server has
  not confirmed.
- Take a filesystem snapshot (ZFS, Btrfs, LVM, a NAS snapshot): it captures every file at the
  same instant.

A copy taken while the server runs can miss the latest batches. The app sends them again:
it moves on only once the server confirms a batch, and the server stores a batch it already
has once.

## Back up with restic

[restic](https://restic.net) makes encrypted backups to a disk, another computer or cloud
storage. These steps back up a Linux binary + systemd install to a disk mounted at
`/mnt/backup`; for another setup, use its archive directory from the table above.

1. Install restic (`sudo apt install restic`, `brew install restic`), then create the backup
   repository once:

    ```sh
    sudo restic init -r /mnt/backup/health
    ```

    restic asks for a password, then prints `created restic repository 1a2b3c4d at
    /mnt/backup/health`. Save the password in your password manager: without it, the backup
    cannot be read.

2. Stop the server, back up, start it again:

    ```sh
    sudo systemctl stop healthkit-relay
    sudo restic -r /mnt/backup/health backup /var/lib/private/healthkit-relay
    sudo systemctl start healthkit-relay
    ```

    The backup ends with `snapshot 5f8e2a1c saved`. With Docker Compose, in the folder with
    `docker-compose.yml`: `docker compose stop relay`, `restic -r /mnt/backup/health backup
    data`, `docker compose start relay`.

3. Check the backup is there:

    ```sh
    sudo restic -r /mnt/backup/health snapshots
    ```

    It lists the snapshot, with its date and the archive directory.

Run step 2 on a schedule, e.g. nightly from cron or a systemd timer.
[BorgBackup](https://www.borgbackup.org), or `rsync -a` to another disk, work the same way.
On a NAS, scheduled snapshots of the share plus the NAS's own backup task cover it.

Health data is sensitive: **encrypt off-site backups** (restic and Borg encrypt by default)
and keep the repository password somewhere other than the server.

## Restore a backup

These steps restore the restic backup above. For another setup, use its archive directory
in step 2; step 3 puts the files back where they were backed up from.

1. Stop the server: `sudo systemctl stop healthkit-relay` (with Docker Compose,
   `docker compose stop relay`).
2. Move the current archive directory aside, so the restore starts from an empty place:

    ```sh
    sudo mv /var/lib/private/healthkit-relay /var/lib/private/healthkit-relay.broken
    ```

3. Restore the latest snapshot to its original place:

    ```sh
    sudo restic -r /mnt/backup/health restore latest --target /
    ```

    restic, run as root, restores the files with their owners.

4. Start the server: `sudo systemctl start healthkit-relay`. Its log shows the archive
   and how many records it holds, e.g. `archive /var/lib/healthkit-relay holds 2731554
   records`.
5. On the iPhone, open Health Relay and tap **Sync Now**. The app shows **Up to date**:
   paired devices and read tokens keep working (`devices.json`), and so does the admin
   token (`admin.json`).
6. Once everything works, delete the `.broken` folder.

The iPhone sends new data at its next sync. Data the server confirmed between the backup and
the loss is missing: the iPhone believes the server has it. To send it again, open the app's
**Advanced** screen (the gear) and tap **Erase Server Data and Start Over**. The server
deletes what this iPhone uploaded, and the iPhone uploads everything its Health app holds,
from the start. Do this when the iPhone's Health app holds the whole history; data only the
backup holds, such as what an older iPhone uploaded, stays as restored.

## Test it

Restore a backup once before you need it. Restore it to a scratch folder and read it with
DuckDB (see [Querying with DuckDB](data.md#querying-with-duckdb)):

```sh
sudo restic -r /mnt/backup/health restore latest --target /tmp/restore-test
sudo -s
cd /tmp/restore-test/var/lib/private/healthkit-relay
duckdb -init /home/you/views.sql -c "SELECT count(*) FROM samples"
```

DuckDB prints the number of samples in the backup. Delete `/tmp/restore-test` afterwards.
