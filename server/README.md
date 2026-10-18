# healthkit-relay server

A single static binary (pure Go, no cgo) that receives Apple Health samples from the Health
Relay app, archives them durably in Parquet files and optionally forwards numbers to a
metrics database.

```sh
go test ./...
CGO_ENABLED=0 go build -o healthkit-relay .
./healthkit-relay -archive-dir /var/lib/healthkit-relay -token-file /run/secrets/hkr-token
```

## Configuration

Every flag can also be set from the environment: `HKR_` plus the flag name in upper case
with dashes as underscores (`-archive-dir` is `HKR_ARCHIVE_DIR`). Flags win.

| Flag | Default | |
|------|---------|-|
| `-listen` | `127.0.0.1:8080` | address to listen on |
| `-archive-dir` | (required) | archive directory, created if missing; also holds `devices.json` |
| `-token-file` | (required) | file holding the server token (at least 16 characters; surrounding whitespace ignored) |
| `-public-url` | (offered, see below) | the URL phones use to reach the server, put in setup links, e.g. `https://health.example.com` |
| `-setup-from` | `private` | who may open `/setup` and pair: `private` (loopback, LAN, Tailscale), `any`, or `off` |
| `-forward-url` | | line-protocol write URL, e.g. `http://127.0.0.1:8428/influx/write` (VictoriaMetrics) or `http://influx:8086/api/v2/write?org=o&bucket=b&precision=ns` (InfluxDB 2) |
| `-forward-token-file` | | bearer token for `-forward-url` |
| `-measurement` | `healthkit` | line-protocol measurement name |
| `-max-body-bytes` | `33554432` | largest accepted request |
| `-flush-interval` | `1h` | how often staged records are written to Parquet |
| `-compact-at` | `03:30` | local time of the nightly compaction |

The token is read from a file, never from a flag value, so it stays out of process
listings. With systemd, `LoadCredential=` and `-token-file ${CREDENTIALS_DIRECTORY}/token`
work well.

## Setting up the app

The **server token** is the admin credential. The app does not need it: it gets a token of
its own by scanning a one-time setup code.

- **Setup page.** Open `https://<server>/setup` in a browser and enter the server token: the
  page shows a QR code to scan with the iPhone's Camera (on the iPhone itself, an **Open in
  Health Relay** button instead), turns into "iPhone connected" once the app has paired, and
  lists paired iPhones with a **Revoke** button. The sign-in lasts an hour (an HttpOnly,
  SameSite=Strict cookie).
- **Terminal.** `healthkit-relay pair` prints the QR code in the terminal, and a link to the
  setup page for that code alone (no token needed to open it; for instance to send to your
  phone). It reads the same flags and `HKR_` variables as the server, and talks to the
  running server on `-listen` (or `-server URL`) with the server token:

  ```sh
  healthkit-relay pair -token-file ./token -public-url https://health.example.com
  healthkit-relay pair -token-file ./token -valid 2h      # a link to send to your phone
  healthkit-relay devices -token-file ./token
  healthkit-relay revoke -token-file ./token 3f9a1c2e
  ```

Which address goes in the code: `-public-url` if set (do set it behind a reverse proxy).
Otherwise the server lists the addresses it can find, best first, and the setup page lets
you pick one (the terminal shows the others):

1. the address the page was opened with, or a local proxy's `X-Forwarded-Host`;
2. Tailscale serve names that proxy to this server (`https://<name>.ts.net:<port>`, read with
   the `tailscale` command if installed);
3. this machine's LAN IPv4 addresses, then its Tailscale IPv4 addresses, if the server
   listens on them (`-listen 0.0.0.0:8080`).

Loopback (`127.0.0.1`, `localhost`) and link-local addresses are never offered: a phone
cannot reach them. When nothing else is found the page says so, with what to set, and shows
no code.

How it works:

- A setup code is 120 random bits, works once, and expires after 15 minutes; codes live in
  memory only, so a restart voids them. The QR code holds
  `healthrelay://pair?server=<public url>&code=<code>`, which the app registers.
- The app sends the code to `POST /v1/pair` and gets a token of its own (256 random bits). The
  server stores only its SHA-256, with the device's name and model, in
  `<archive-dir>/devices.json` (mode 600). Revoking deletes the entry: the token stops
  working at once. Pairing again from the same iPhone revokes its previous token.
- `/setup` and `/v1/pair` answer only clients on private networks by default
  (`-setup-from`); behind a reverse proxy on the same host, the address the proxy puts last in
  `X-Forwarded-For` counts. Without the server token or a valid code, the page shows only its
  sign-in form. Responses are not cached, send no referrer and cannot be framed.
- The server token still authorizes uploads, so an app set up by hand keeps working (app
  0.4 swaps it for a device token of its own on the first start).

## API

### `POST /v1/samples`

Header `Authorization: Bearer <token>`. Body:

```json
{
  "device": {"model": "iPhone17,1", "system_version": "26.0", "app_version": "0.1.0"},
  "samples": [
    {"uuid": "1B4E28BA-2FA1-11D2-883F-0016D3CCA427",
     "type": "HKQuantityTypeIdentifierHeartRate", "kind": "quantity",
     "start": "2026-01-02T03:04:05.123Z", "end": "2026-01-02T03:04:05.123Z",
     "value": 62, "unit": "count/min",
     "source": {"name": "Apple Watch", "bundle_id": "com.apple.health.XXXX", "version": "26.0", "device": "Watch"},
     "metadata": {"HKMetadataKeyHeartRateMotionContext": "1"}},
    {"uuid": "…", "type": "HKCategoryTypeIdentifierSleepAnalysis", "kind": "category",
     "start": "…", "end": "…", "value": 4, "value_label": "asleepDeep", "source": {"name": "Apple Watch"}},
    {"uuid": "…", "type": "HKWorkoutTypeIdentifier", "kind": "workout", "start": "…", "end": "…",
     "source": {"name": "Apple Watch"},
     "workout": {"activity_type": 37, "activity_name": "running", "duration_s": 1800,
                 "energy_kcal": 310.2, "distance_m": 5012}}
  ],
  "deletions": [
    {"uuid": "…", "type": "HKQuantityTypeIdentifierStepCount"}
  ]
}
```

Required per sample: `uuid`, `type`, `start`, `end` (RFC 3339). Everything else is
optional, and unknown fields are archived as sent, so newer clients need no server change.

That is wire format 1, which app 0.1 sends. **Version 2** (app 0.2) adds `"version": 2` and
a `records` array, and sends every HealthKit kind:

```json
{
  "version": 2,
  "device": {"model": "iPhone17,5", "system_version": "27.0", "app_version": "0.2.0", "sdk": "iOS 27.0 SDK"},
  "samples": [
    {"uuid": "…", "type": "HKQuantityTypeIdentifierHeartRate", "kind": "quantity", "start": "…", "end": "…",
     "value": 62, "unit": "count/min", "aggregation": {"raw": 2, "name": "discreteTemporallyWeighted"},
     "source": {"name": "Apple Watch", "bundle_id": "…", "version": "27.0", "product_type": "Watch7,1", "os_version": "27.0.0"},
     "device": {"name": "Apple Watch", "manufacturer": "Apple Inc.", "model": "Watch", "hardware_version": "Watch7,1", "software_version": "27.0"},
     "metadata": {"HKMetadataKeyHeartRateMotionContext": 1}},
    {"uuid": "…", "type": "HKDataTypeIdentifierElectrocardiogram", "kind": "electrocardiogram", "start": "…", "end": "…",
     "classification": {"raw": 1, "name": "sinusRhythm"}, "average_heart_rate": {"value": 61, "unit": "count/min"},
     "number_of_voltage_measurements": 15360, "archive": "YnBsaXN0MDDUAQID…", "source": {"name": "ECG"}}
  ],
  "records": [
    {"kind": "series", "key": "<ecg uuid>/electrocardiogram/0", "series": "electrocardiogram", "sample_uuid": "…",
     "index": 0, "final": true, "total_points": 15360, "unit": "mcV", "points": [{"t": 0.0, "v": -12.5}, …]},
    {"kind": "activity_summary", "key": "2026-10-17", "date": "2026-10-17", "start": "2026-10-17T04:00:00.000Z",
     "values": {"active_energy_burned": {"value": 512, "unit": "kcal"}, …}}
  ],
  "deletions": [{"uuid": "…", "type": "HKQuantityTypeIdentifierStepCount"}]
}
```

Sample kinds (`kind`): `quantity`, `category`, `workout`, `correlation` (members in `objects`),
`workout_route`, `heartbeat_series`, `electrocardiogram`, `audiogram`, `state_of_mind`,
`scored_assessment`, `medication_dose_event`, `vision_prescription`, `clinical_record` (FHIR
JSON in `fhir.data`), `cda_document`, and `sample` for anything newer. Enum values are
`{"raw": n, "name": "…"}` with the SDK's case name. Metadata keeps JSON types; dates are RFC
3339 strings and quantities `{"value", "unit"}`. `archive` is the object's NSKeyedArchiver
archive (base64), on every kind except `quantity` and `category`.

Record kinds: `series` (points of a route, heartbeat series, ECG or quantity series, in
chunks; the last has `final: true`), `blob` (base64 chunks of a CDA document or an attachment),
`activity_summary` (one per day), `characteristics`, `medication`,
`workout_effort_relationship`. Required per record: `kind` (`[a-z][a-z0-9_]*`) and `key`
(1 to 512 bytes). A record's series and blob chunks are always sent before (or with) the
sample they belong to.

Responses:

- `200` `{"received": 4, "stored": 3, "duplicates": 1, "deletions": 1, "records": 0, "version": 2}`:
  the batch is on disk. `records` tells a client the server stores records (a v1 server
  would silently drop them).
- `400` invalid JSON or an invalid record. Nothing from the batch is stored.
- `401` missing, wrong or revoked token. `413` body too large. `500` the archive could not be
  written.

The token is the server token or a device token from pairing.

### `POST /v1/pair`

No token; the setup code is the credential. Body `{"code": "…", "name": "iPhone", "model":
"iPhone17,1", "replaces": "<previous device token, optional>"}`. `200`
`{"token": "…", "device": {"id": "3f9a1c2e", "name": "iPhone", "model": "iPhone17,1",
"created_at": "…"}, "server": {…status…}}`; `403` for an unknown, used or expired code, or a
client outside `-setup-from`.

### `GET /v1/status`

Any valid token. `{"version": "…", "wire_version": 2, "records": 12571973, "device": {…}}`
(`device` only for a device token). The app shows the record count and detects a revoked
token here.

### `POST /v1/pairing-codes`, `GET /v1/devices`, `DELETE /v1/devices/{id}`

Server token only: mint a setup code (`{"code", "expires_at", "link", "setup_url"}`; the body
may give `{"public_url": "…"}` when `-public-url` is not set), list paired devices, revoke
one (`204`). The `pair`, `devices` and `revoke` commands use these.

A batch is stored entirely or not at all, so a client can safely retry until it gets 200.

### `DELETE /v1/data`

Erases, permanently, everything the caller uploaded: with a device token, that device's
records; with the server token, what was uploaded with the server token, or with
`?device=<id>` what that device uploaded (also after it was revoked). `200`
`{"erased": 12571973, "device": "3f9a1c2e"}`. The app's **Erase Server Data and Start Over**
calls it, then uploads its history again from scratch. See [Erasing](#erasing).

### `GET /healthz`

`200 ok` while the process runs.

### `GET /metrics`

Prometheus text format: `healthkit_relay_requests_total{result}` (`ok`, `unauthorized`,
`bad_request`, `incomplete` for a body cut short by a client going away, `archive_error`,
`paired`, `pair_rejected`, `erased`), `healthkit_relay_records_total{outcome}`,
`healthkit_relay_archive_records`, `healthkit_relay_staged_records` (not flushed yet),
`healthkit_relay_last_success_timestamp_seconds{job="flush"|"compaction"}` (alert when it
gets old: more than two hours for the flush, a day and a half for the compaction),
`healthkit_relay_maintenance_errors_total{job}`, `healthkit_relay_devices`,
`healthkit_relay_forward_errors_total`, `healthkit_relay_last_upload_timestamp_seconds`.
No health data.

## Archive

Everything lives in `-archive-dir`:

```
staging.db          SQLite: batches not flushed yet, the de-duplication index, tombstones
devices.json        paired devices (token hashes)
parquet/
  samples/type=HKQuantityTypeIdentifierHeartRate/month=2023-03/c1234.parquet
  samples/type=…/month=…/f5678.parquet        a flush not compacted yet
  records/type=activity_summary/month=2026-10/…
  route_points/type=HKWorkoutRouteTypeIdentifier/month=…/…
  heartbeats/type=HKDataTypeIdentifierHeartbeatSeries/month=…/…
  ecg_voltages/type=HKDataTypeIdentifierElectrocardiogram/month=…/…
  quantity_series/type=HKQuantityTypeIdentifierStepCount/month=…/…
  */type=_/month=_/schema.parquet              no rows: queries work before any data arrives
  deleted/deleted.parquet
```

**Write path.** A batch is validated whole, then committed to `staging.db` in one
transaction (fsynced) before the app gets its `200`. De-duplication is a table of the IDs of
every record ever stored (16 bytes of SHA-256 each, on disk), so memory stays the same however
long the history: samples are unique per `(wire format, uuid)`, records per
`(kind, key, SHA-256 of the record as sent)`, tombstones per `uuid`. A v2 copy of a sample a v1
client sent supersedes it; a v1 copy of a sample already stored in v2 is a duplicate.

**Flush, hourly.** For each `(dataset, type, month)` in staging, the rows are streamed into a
new Parquet file, `f<seq>.parquet` (a temp file, fsynced, renamed), then deleted from staging.
The flush is journaled: one cut short by a crash is replayed with the same name on the next
start, replacing what it wrote, so nothing is lost or duplicated. The tombstones are exported
to `deleted/deleted.parquet` at each flush.

**Compaction, nightly.** Each partition with more than one file is merged into one,
`c<seq>.parquet`, streaming a row group at a time (100 000 rows, compressed), and the rows of
deleted samples are dropped on the way. Months that received nothing are never rewritten.
Journaled the same way: a merged file renamed into place means its inputs are removed, even
after a crash. Memory stays at a few tens of MB however large the archive
([memory_test.go](memory_test.go)).

**Partitions.** The month is that of the sample's start, in UTC. Series chunks go with their
sample (a chunk may arrive in a batch before its sample's: it waits in staging, at most a day,
then goes to the month it was received in). Types and kinds name directories: characters other
than `[A-Za-z0-9._-]` are %-escaped.

**Schema.** `samples` and `records` share one: `uuid`, `type`, `kind`, `key` (records),
`start`, `end` (timestamps, UTC), `value` (double), `unit`, `source_name`, `source_bundle`,
`source_version`, `device`, `uploader` (the paired device's ID, `""` for the server token),
`wire` (1 or 2), `received_at`, and `json`, the record exactly as sent: nothing is lost, and
fields added by newer apps are kept. In `records`, `type` is the record kind and `uuid` the
sample a chunk belongs to. The point datasets have one row per point: `sample_uuid`, `chunk`,
`i`, `time`, then `lat`, `lon`, `alt`, `h_acc`, `v_acc`, `speed`, `speed_acc`, `course`,
`course_acc`, `ellipsoidal_alt`, `floor` (routes); `t` (seconds since the sample's start) and
`gap` (heartbeats); `t` and `microvolts` (ECG, lead I); `start`, `end`, `value`, `unit`
(quantity series). A series chunk is also kept whole in `records` (type `series`).

**Deletions.** HealthKit tombstones go to the `deleted` table (and `deleted.parquet`, with
`wire` 0); v1 copies superseded by v2 are listed there with `wire` 1. Their rows are dropped
when their partition is next compacted; until then the views below hide them. HealthKit never
reuses UUIDs; an edited sample arrives as a deletion plus a new UUID.

### Querying with DuckDB

[`sql/views.sql`](sql/views.sql) defines views that read the Parquet files and hide deleted
rows: `samples`, `records`, `records_latest`, `route_points`, `heartbeats`, `ecg_voltages`,
`quantity_series`, `deleted`. Start DuckDB in the archive directory with it:

```sh
cd /var/lib/healthkit-relay && duckdb -readonly -init /path/to/server/sql/views.sql
```

```sql
-- How much of what
SELECT type, count(*) AS n, min(start), max(start) FROM samples GROUP BY type ORDER BY n DESC;

-- Daily resting heart rate, all years (the partition filter reads only those files)
SELECT date_trunc('day', timezone('America/New_York', start)) AS day, avg(value) AS bpm
FROM samples WHERE type = 'HKQuantityTypeIdentifierRestingHeartRate' GROUP BY day ORDER BY day;

-- Hours asleep per night, by stage
SELECT date_trunc('day', timezone('America/New_York', "end")) AS night, json_extract_string(json, '$.value_label') AS stage,
       round(sum(epoch("end") - epoch(start)) / 3600, 2) AS hours
FROM samples WHERE type = 'HKCategoryTypeIdentifierSleepAnalysis' GROUP BY ALL ORDER BY night, stage;

-- Fields that have no column: any key of the sample as sent
SELECT start, value, json_extract(json, '$.metadata.HKMetadataKeyHeartRateMotionContext') AS context
FROM samples WHERE type = 'HKQuantityTypeIdentifierHeartRate' AND month = '2026-03' LIMIT 10;

-- A workout's route, and its heart rate
SELECT time, lat, lon, alt, speed FROM route_points WHERE sample_uuid = '<route uuid>' ORDER BY chunk, i;

-- Beat-to-beat intervals of the last heartbeat series
SELECT time, t - lag(t) OVER (ORDER BY chunk, i) AS rr_s FROM heartbeats
WHERE sample_uuid = (SELECT uuid FROM samples WHERE type = 'HKDataTypeIdentifierHeartbeatSeries' ORDER BY start DESC LIMIT 1);

-- Activity rings, latest version of each day
SELECT json_extract_string(json, '$.date') AS day,
       json_extract(json, '$.values.active_energy_burned.value')::double AS kcal
FROM records_latest WHERE type = 'activity_summary' ORDER BY day;
```

Records received in the last hour may still be in `staging.db` only (`healthkit-relay flush`
writes them out at once, with the server stopped).

### Erasing

`DELETE /v1/data` (see above) erases, with no soft delete and no copy kept: Parquet files
holding only that uploader's rows are removed, others are rewritten without them (a temp file
renamed over the original), its staged rows, tombstones and de-duplication entries are
deleted (SQLite overwrites deleted content, `secure_delete`), and the SQLite log is truncated.
It is journaled like flush and compaction: an erase a crash interrupts is finished on the next
start, before uploads are accepted. Uploads wait while it runs. Copies outside the archive
directory (backups, filesystem snapshots) are yours to handle.

### Commands

With the server stopped (they lock the archive directory, as the server does):

```sh
healthkit-relay flush   -archive-dir /var/lib/healthkit-relay   # write staged records to Parquet now
healthkit-relay compact -archive-dir /var/lib/healthkit-relay   # flush, then compact now
healthkit-relay migrate -archive-dir /var/lib/healthkit-relay   # from the JSON Lines archive of 0.4 and earlier
```

`migrate` streams the `YYYY/YYYY-MM-DD.jsonl` files of earlier versions through the same path
as uploads (constant memory), keeps a sample sent in both v1 and v2 once (as v2), checks that
the rows in Parquet match what it stored for every type, then compacts. Records are attributed
to `-uploader <device id>`, by default the paired device if there is exactly one (the old
archive did not record who uploaded what). It leaves the JSON Lines files in place: check the
counts, then delete them.

## Metrics forwarding

New samples (not duplicates, and not v2 copies of a sample already forwarded in v1) that
carry a number are posted as line protocol after they are archived, as are new daily
activity summaries. Other kinds (routes, ECGs, correlations...) stay in the archive. A forwarding failure is logged and counted but does not fail the upload:
the archive stays the source of truth.

| Sample | Line |
|--------|------|
| quantity | `healthkit,source=…,unit=count/min heart_rate=62 <start ns>` |
| category | `healthkit,source=…,value=asleepDeep sleep_analysis_seconds=1800 <start ns>` (duration) |
| workout | `healthkit,activity=running,source=… workout_duration_seconds=…`, `workout_energy_kcal`, `workout_distance_meters` |
| activity summary | `healthkit,unit=kcal activity_summary_active_energy_burned=512 <day start ns>`, one per ring and goal |

Field names are the HealthKit identifier in snake case
(`HKQuantityTypeIdentifierHeartRateVariabilitySDNN` → `heart_rate_variability_sdnn`).
VictoriaMetrics names the series `<measurement>_<field>`, e.g. `healthkit_heart_rate`.
Note that a metrics database with a short retention keeps only recent data from a full
history upload; the archive keeps everything.
