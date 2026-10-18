# Backups

Your phone keeps Apple Health, and iCloud may too, but the server is the only copy in a form
you control. Back up its data directory.

## What to back up

The whole data directory (`-archive-dir`): the Parquet archive, the staging database and
`devices.json`. The server token file too, or be ready to make a new one.

The metrics database (VictoriaMetrics, InfluxDB) does not need a backup for this project's
sake: everything forwarded to it is also in the archive.

## Consistent copies

Parquet files are written once and never modified, so copying them is always safe. The
staging database changes during uploads; for a consistent copy, either:

- stop the server for the copy (`systemctl stop healthkit-relay` or `docker compose stop`),
  which is fine: the phone retries later and nothing is lost; or
- take a filesystem snapshot (ZFS, Btrfs, LVM, a NAS snapshot), which is atomic.

Even a copy taken while running loses at most the latest batches, which the app re-sends:
it only forgets what the server has confirmed, and the server ignores what it already has.

## Examples

[restic](https://restic.net) to any storage, encrypted:

```sh
systemctl stop healthkit-relay
restic -r /mnt/backup/health backup /var/lib/healthkit-relay
systemctl start healthkit-relay
```

[BorgBackup](https://www.borgbackup.org), or a plain `rsync -a` to another disk, work the
same way. On a NAS, scheduled snapshots of the dataset or share plus the NAS's own backup
task are enough.

Health data is sensitive: **encrypt off-site backups** (restic and Borg do by default) and
keep the repository password somewhere other than the server.

## Restoring

Stop the server, put the data directory back, start the server. Paired phones keep working
(their token hashes are in `devices.json`) and send new data at their next sync. Data the
server confirmed between the backup and the loss is not re-sent on its own, since the phone
believes the server has it. To fill that gap, re-send all history from the app (Advanced);
the server ignores what it already has.

## Test it

A backup you have never restored is a hope. Once, restore to a scratch directory and query
it with DuckDB (see [Data model & querying](data.md)).
