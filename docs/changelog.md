# Changelog

Notable changes to the server and the app. Versions follow [Semantic Versioning](https://semver.org);
server releases and the app are versioned separately.

## Unreleased

### App 0.6.2

- Background sync that keeps running: every HealthKit wake syncs all types, in one sync
  shared by all the wakes that arrive together (opening the app or an unlock fires a few
  hundred), where 0.6.1 started one sync per type. iOS counts each wake against the app's
  background time, and stops waking an app that does not answer HealthKit.
- When iOS ends the app's background time, the app answers HealthKit's open wakes and stops
  the sync, which the next wake finishes. In 0.6.1 a sync cut short waited, unanswered,
  until iOS ran the app again, which could be hours.
- HealthKit may wake the app at once for new heart rate, steps, active energy, walking
  distance and sleep data, so it runs soon after an unlock; other types stay at most hourly.
- A heartbeat to the server at the end of each background wake, also when the iPhone was
  locked or the upload failed: what woke the app, whether Health was readable, the outcome,
  the last error and the background time left. See
  [No syncs in the background](troubleshooting.md#no-syncs-in-the-background).

### Server

- `POST /v1/heartbeat` (an upload token): logs the app's heartbeat in one line, and counts
  it in `healthkit_relay_heartbeats_total{trigger}` and
  `healthkit_relay_last_heartbeat_timestamp_seconds{trigger}`.
- `healthkit_relay_last_batch_timestamp_seconds{trigger}`: when the last batch of each
  trigger arrived.
- Refused tokens are logged (at most every 10 seconds, with a count of the others): method,
  path, client address, user agent and a 6-character prefix of the token's SHA-256.
- Query API, turned on with `-query-duckdb`: read-only endpoints for the type catalog,
  samples and daily or hourly aggregates, with time zones and Apple-style source
  de-duplication. The binary serves an OpenAPI 3.1 document at `/openapi.json` and Swagger UI
  at `/docs`. Each query runs in a locked-down `duckdb` process. See
  [Query API](query-api.md).
- The OpenAPI document describes `type` as a string with a pattern and examples, in place of
  an enum of every HealthKit identifier; `GET /v1/types` lists the valid values. An unknown
  type answers `400` with the closest names: `unknown type HKQuantityTypeIdentifierSteps; did
  you mean HKQuantityTypeIdentifierStepCount?`. Kinds stay an enum, each described in one
  line. `GET /v1/kinds` is removed: `GET /v1/types?kind=category` lists the types of one
  kind. The served document goes from 67 kB to 54 kB.
- Token scopes: a read token (from the setup page, `healthkit-relay token create` or
  `POST /v1/tokens`) queries and does nothing else; a paired device's token uploads and gets
  `403` on queries. Existing `devices.json` files keep working as they are.
- The admin token (formerly the server token): the server generates it on first start, 256
  random bits, and prints it once to its log with the setup page's address
  (`docker compose logs relay`, `journalctl -u healthkit-relay`). `<archive-dir>/admin.json`
  holds its SHA-256.
- `healthkit-relay admin reset` prints a new admin token; the running server switches to it
  at once and signs out browsers signed in with the old one.
- `pair`, `devices`, `revoke`, `token create` and `admin reset` work on the state files in
  the archive directory (`-archive-dir`), with file locks, while the server runs; this
  replaces their `-token-file` and `-server` flags. Run them as the server's user or as root,
  e.g. `docker compose exec relay healthkit-relay pair`.
- Setup codes are kept as SHA-256 in `codes.json` and last across a restart until they
  expire.
- `-token-file` (`HKR_TOKEN_FILE`) is optional and deprecated: at start, the server imports
  an existing server token file into `admin.json` and logs a warning; then delete the file
  and the flag. The Compose file mounts no token file; the image still imports
  `/run/secrets/hkr_token` when an earlier install mounts one.
- Uploads and status requests with the admin token are deprecated: still accepted, logged at
  most hourly and counted in `healthkit_relay_admin_token_uploads_total`. Pair such apps
  again from the setup page.
- Setup page: **Sign in** with the admin token, with where to find it; **Pair your iPhone**,
  **Paired devices** and **Read tokens**; copy buttons for a new read token and the setup
  code's link.
- Reworded output: the first-start log says `Setup page:` (formerly `Sign in at:`), and the
  `devices` column `PAIRED` is now `CREATED`.
- NixOS: `healthkit-relay-ctl` runs any command with the service's settings
  (`sudo healthkit-relay-ctl pair`, `admin reset`, ...). `tokenFile` is deprecated; the
  module imports a `/var/lib/healthkit-relay/token` from its earlier versions once.

### App

- Welcome, pairing, Health access, status and error texts use the server's terms (server
  address, setup page, setup code, admin token, paired device), and each error says what to
  do next. Manual setup asks for the **Admin token**.

## 0.6.1 (unreleased)

### Server

- Batches may say what started the app's sync (`trigger`) and the app's state
  (`app_state`): both appear in the `batch v2:` log line, and
  `healthkit_relay_batches_total{trigger}` counts batches by trigger (`unknown` for older
  apps).

### App 0.6.1

- Background sync works: observer queries are set up synchronously at launch from the types
  the app asked for (saved when it asks), with no HealthKit status call first, so a launch
  on a locked phone no longer ends with nothing observed.
- An authorization plan or readable-type list HealthKit could not fully answer (phone
  locked) is no longer kept for the life of the process; it is computed again at unlock and
  when the app opens.
- A sync skipped while locked, or failed, is remembered across launches and run at the next
  unlock, background refresh or open.
- A background app refresh about hourly runs a full incremental sync as a fallback.
- Each batch says what started it (`foreground`, `observer`, `refresh`, `unlock`, `retry`).
  Each observer wake is logged with the app's state and whether protected data is readable.

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
