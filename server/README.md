# healthkit-relay server

A single static binary (Go standard library only) that receives Apple Health samples from
the Health Relay app, archives them durably and optionally forwards numbers to a metrics
database.

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

### `GET /healthz`

`200 ok` while the process runs.

### `GET /metrics`

Prometheus text format: `healthkit_relay_requests_total{result}` (`ok`, `unauthorized`,
`bad_request`, `incomplete` for a body cut short by a client going away, `archive_error`,
`paired`, `pair_rejected`), `healthkit_relay_records_total{outcome}`,
`healthkit_relay_archive_records`, `healthkit_relay_devices`,
`healthkit_relay_forward_errors_total`, `healthkit_relay_last_upload_timestamp_seconds`.
No health data.

## Archive

`<archive-dir>/YYYY/YYYY-MM-DD.jsonl`, one file per UTC day of receipt, one record per line:

```json
{"op":"sample","uuid":"1B4E28BA-…","type":"HKQuantityTypeIdentifierHeartRate","received_at":"2026-10-17T16:00:00Z","data":{…sample as sent…}}
{"op":"sample","uuid":"1B4E28BA-…","v":2,"type":"HKQuantityTypeIdentifierHeartRate","received_at":"…","data":{…}}
{"op":"record","type":"activity_summary","key":"2026-10-17","digest":"9f86d081884c7d65…","received_at":"…","data":{…}}
{"op":"delete","uuid":"3B4E28BA-…","type":"HKQuantityTypeIdentifierStepCount","received_at":"…","data":{…}}
```

- Files are only appended to, and each write is fsynced before the response.
- Samples are stored once per `(v, uuid)`: a v2 client re-sending history that a v1 client
  already sent adds the fuller copy once (prefer `v: 2` when both exist). Tombstones are
  stored once per `uuid`. Records are stored once per `(type, key, digest)`, where `digest`
  is a SHA-256 of the record as sent: a changed activity summary is a new line, an unchanged
  one is a duplicate; take the latest `received_at` per key.
- On start the server reads the archive to rebuild this index (memory grows with record
  count: roughly 100 bytes per record).
- A sample is current unless a `delete` record with the same `uuid` exists, in any file.
  HealthKit never reuses UUIDs; edited samples arrive as a deletion plus a new UUID.
- A line cut short by a crash is skipped on the next start; the client re-sends it.

Examples: every heart-rate value received (v2 copies only), and the points of one route:

```sh
cat archive/*/*.jsonl | jq -r 'select(.op=="sample" and .v==2 and .type=="HKQuantityTypeIdentifierHeartRate") | [.data.start, .data.value, .data.unit] | @tsv'
cat archive/*/*.jsonl | jq -c 'select(.op=="record" and .type=="series" and .data.sample_uuid=="<route uuid>") | .data.points[]'
```

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
