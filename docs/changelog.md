# Changelog

Notable changes to the server and the app. Versions follow [Semantic Versioning](https://semver.org);
server releases and the app are versioned separately.

## 0.6.0 (2026-10-18)

### Server

- Erasing requires `?confirm=erase`; only the app before 0.6 may leave it out.
- An archive directory, or `parquet/` in it, that is a symlink works (systemd's
  `DynamicUser` makes `/var/lib/<name>` one): de-duplication and file modes no longer miss
  what is behind it. File modes are set only on what the store owns.
- Line protocol escapes backslashes and control characters; paired device names refuse
  control characters; the setup page refuses cross-origin form posts, caps form size and
  cannot be framed.
- Packaging: Docker image, release binaries with a systemd unit, a NixOS module with a VM
  test, and an end-to-end smoke test run on every install path in CI.

### App 0.6.0

- Asks before sending Health data to a server it was not set up with, when a setup link
  names a new one.
- Sends `confirm=erase` with **Erase Server Data and Start Over**.

### Docs

- Documentation site, setup guides, security policy and contribution guidelines.

## 0.5 (2026-10-17)

### Server 0.5.0 to 0.5.2

- Storage: a SQLite staging database (durable before the `200`, de-duplication on disk),
  flushed hourly to Parquet partitioned by dataset, type and month, compacted nightly, with
  deletions applied at compaction. DuckDB views in `server/sql/views.sql`.
- `migrate` converts the JSON Lines archive of 0.4 and earlier; `flush` and `compact` run
  the maintenance at once.
- `DELETE /v1/data` erases what a device uploaded, for good.

### App 0.5.0

- **Erase Server Data and Start Over**.

## 2026-10-17

### App 0.4.1

- One setup change at a time, so a setup link opened at launch leaves a single paired device.

### Server

- `pair -valid <duration>` for setup codes sent to a phone (default 15 minutes, at most a
  day).
- Loopback addresses are never put in a setup code; the setup page offers the addresses it
  finds (the page's own address, Tailscale serve names, LAN and Tailscale IPs).
- One-tap app setup: one-time setup codes, a QR code on `/setup` and in the terminal
  (`pair`), and per-device tokens that can be listed (`devices`) and revoked (`revoke`).
- Records whose values are not `{value, unit}` are accepted; rejected batches are logged with
  the reason.

### App 0.4.0

- A status screen, setup from a link or QR code, Health access in one step.

### App 0.3.0

- No HealthKit exception can end the app; Health access is one sheet, per-object types are
  optional rows. Health Records became an opt-in step.

### 0.2

- Everything in HealthKit is synced, with the type list generated from the iOS SDK at build
  time (wire format version 2: workouts with routes, ECGs, heartbeat series, correlations,
  documents, activity summaries, characteristics).

### 0.1

- First version: a Go receiver with an append-only archive and line-protocol forwarding, and
  a SwiftUI app that syncs HealthKit incrementally in the background.
