# healthkit-relay server

The `healthkit-relay` server is one static binary, in pure Go (built with `CGO_ENABLED=0`).
It receives Apple Health samples from the Health Relay iPhone app, keeps them in the archive
(Parquet files in a folder you choose) and can forward numbers to a metrics database.

```sh
go test ./...
CGO_ENABLED=0 go build -o healthkit-relay .
./healthkit-relay -archive-dir /var/lib/healthkit-relay   # prints the admin token on first start
```

## First start

1. Start the server with a folder for its data and the address your iPhone uses to reach
   it (the server address):

   ```sh
   ./healthkit-relay -archive-dir ./data -listen 0.0.0.0:8080 -public-url http://192.168.1.10:8080
   ```

2. The log shows the admin token, once, and the setup page's address:

   ```text
   First start: the server made its admin token. The admin token is the password of this server. It is shown only this once.

       Admin token: Zk3v...
       Setup page:  http://192.168.1.10:8080/setup
   ```

   Save the admin token in a password manager.

3. Open the setup page in a browser and sign in with the admin token. Point the iPhone's
   Camera at the QR code and tap **Open in Health Relay**. The page shows **iPhone paired**.

## Configuration

Every flag can also be set from the environment: `HKR_` plus the flag name in upper case
with dashes as underscores (`-archive-dir` is `HKR_ARCHIVE_DIR`). Flags win over the
environment.

| Flag | Default | |
|------|---------|-|
| `-listen` | `127.0.0.1:8080` | address and port to listen on; `0.0.0.0:8080` also answers other computers and phones |
| `-archive-dir` | (required) | the archive: the folder where the server keeps your data, created if missing; also holds `admin.json`, `devices.json` and `codes.json` |
| `-token-file` | | deprecated: file holding the token of earlier versions (formerly the server token, at least 16 characters), imported as the admin token at start; then delete it |
| `-public-url` | (offered, see below) | the server address: the address iPhones use to reach the server, put in setup codes, e.g. `http://192.168.1.10:8080` or `https://health.example.com` |
| `-setup-from` | `private` | who may open the setup page (`/setup`) and pair: `private` (this computer, the local network, Tailscale), `any`, or `off` (pairing is off too) |
| `-forward-url` | | line-protocol write URL, e.g. `http://127.0.0.1:8428/influx/write` (VictoriaMetrics) or `http://influx:8086/api/v2/write?org=o&bucket=b&precision=ns` (InfluxDB 2) |
| `-forward-token-file` | | bearer token for `-forward-url` |
| `-measurement` | `healthkit` | line-protocol measurement name |
| `-max-body-bytes` | `33554432` | largest accepted request body, in bytes |
| `-flush-interval` | `1h` | how often staged records are written to Parquet |
| `-compact-at` | `03:30` | local time of the nightly compaction |
| `-query-duckdb` | (off) | `duckdb` executable for the read-only query API, `/openapi.json` and `/docs`; see [docs/query-api.md](../docs/query-api.md) for it and the other `-query-*` flags |
| `-demo-code-file` | | turns this server into a public demo server: file holding a shared setup code (16 or more characters of A-Z and 2-7) that pairs any number of iPhones; see [Demo server](#demo-server) |
| `-demo-reset` | `1h` | demo server: how often every record is erased |
| `-demo-quota` | `268435456` | demo server: most upload bytes accepted between two erases |
| `-group-read` | off | make the archive readable by the service's group (directories 0750, files 0640), for a backup job or a query tool; by default only the service's user can read it (0700, 0600), set at every start |
| `-version` | | print the version and exit |

The Go runtime's soft memory limit defaults to 128 MiB (`GOMEMLIMIT` overrides it): the
server holds a batch and a Parquet row group at a time, whatever the archive's size.

## Admin token

The **admin token** is the password of your server. It signs in to the setup page (`/setup`),
where you pair iPhones and create and revoke read tokens. It also authorizes the admin API
(`POST /v1/pairing-codes`, `GET /v1/devices`, `DELETE /v1/devices/{id}`, `POST /v1/tokens`),
the query API, and erasing any paired device's data. On first start with no `admin.json`,
the server makes one (256 random bits) and keeps its SHA-256 in `<archive-dir>/admin.json`
(mode 600):

```json
{"token_sha256": "9f86d0…", "created_at": "2026-10-19T12:00:00Z"}
```

It prints the token once, on the line `    Admin token: <token>`, with the setup page's
address on the line `    Setup page:  <address>` (`-public-url` + `/setup`, else
`http://<listen>/setup`, with `127.0.0.1` for `0.0.0.0`), to stderr: `docker compose logs
relay`, `journalctl -u healthkit-relay`, or the terminal running it. `healthkit-relay admin
reset` makes a new one and prints it the same way, once, to stdout. The running server
rereads `admin.json` when it changes: the old token stops working at once, and browsers
signed in with it are signed out.

From earlier versions: `-token-file` (`HKR_TOKEN_FILE`), the token formerly called the server
token, is still read at start. With no `admin.json` yet, the server imports the token's hash
and logs a deprecation warning: that token is now the admin token. Save it in a password
manager, delete the file and remove `-token-file`. With an `admin.json` holding another
token (after `admin reset`), the server ignores the file and logs a warning.

## Setting up the app

The app gets a token of its own, for that iPhone only, by scanning a setup code: a one-time
code that works for 15 minutes. The admin token stays with you.

- **Setup page.** Open the setup page (the server address + `/setup`, e.g.
  `http://192.168.1.10:8080/setup`) in a browser and sign in with the admin token. The page
  shows a QR code to scan with the iPhone's Camera (on the iPhone itself, an **Open in Health
  Relay** button), turns into **iPhone paired** once the app has paired, and lists **Paired
  devices** and **Read tokens**, each with a **Revoke** button. On a computer it also shows
  the setup code's link, with a copy button. The sign-in lasts an hour (an HttpOnly,
  SameSite=Strict cookie).
- **Terminal.** `healthkit-relay pair` prints the QR code in the terminal, and a link to the
  setup page for that setup code alone. That link opens without the admin token, so you can
  send it to your phone.

## Commands

`pair`, `devices`, `revoke <id>`, `token create -name X [-scope read|upload]` and
`admin reset` read the same flags and `HKR_` variables as the server and act on the state
files in the archive directory (`devices.json`, `codes.json`, `admin.json`), with file locks,
while the server runs; the server rereads the files when they change. Run them as the
server's user or as root (as root, the files keep the owner of the archive directory):

```sh
healthkit-relay pair -archive-dir ./data -public-url https://health.example.com   # a setup code: QR code and link
healthkit-relay pair -archive-dir ./data -valid 2h      # one that works for 2 hours, to send its link to your phone
healthkit-relay devices -archive-dir ./data             # lists paired devices and read tokens, with their IDs
healthkit-relay revoke -archive-dir ./data 3f9a1c2e     # its token stops working at once
healthkit-relay token create -archive-dir ./data -name grafana   # a read token, printed once on stdout
healthkit-relay admin reset -archive-dir ./data         # a new admin token; the old one stops working

docker compose exec relay healthkit-relay pair          # Docker: HKR_ARCHIVE_DIR=/data is set
sudo healthkit-relay-ctl pair                           # NixOS: the service's settings
```

Which server address goes in the setup code: `-public-url` if set (set it behind a reverse
proxy). Otherwise the server lists the addresses it can find, best first, and uses the
first. When it finds several, the setup page lets you pick one under **Server address in
the code**, and `pair` lists the others:

1. the address the page was opened with, or a local proxy's `X-Forwarded-Host`;
2. Tailscale serve names that proxy to this server (`https://<name>.ts.net`, with `:<port>`
   unless it is 443, read with the `tailscale` command if installed);
3. this machine's LAN IPv4 addresses, then its Tailscale IPv4 addresses, if the server
   listens on them (`-listen 0.0.0.0:8080`).

The list holds only addresses a phone can reach: loopback (`127.0.0.1`, `localhost`) and
link-local addresses stay out. When the list is empty, the setup page shows **Set the server
address** with what to set, and `pair` exits with an error that says the same.

How it works:

- A setup code is 120 random bits, works once, and expires after 15 minutes (`pair -valid`,
  up to a day). The server keeps its SHA-256 in `<archive-dir>/codes.json`, so it survives a
  restart until it expires. The QR code holds the setup code's link,
  `healthrelay://pair?server=<server address>&code=<code>`, which the app registers.
- The app sends the code to `POST /v1/pair` and gets a token of its own (256 random bits): it
  is now a paired device. The server stores only the token's SHA-256, with the device's name
  and model, in `<archive-dir>/devices.json` (mode 600). Revoking deletes the entry: the
  token stops working at once. Pairing again from the same iPhone replaces its previous
  token and keeps its device ID.
- `/setup` and `/v1/pair` answer only clients on private networks by default
  (`-setup-from`); behind a reverse proxy on the same host, the address the proxy puts last in
  `X-Forwarded-For` counts. Without the admin token or a valid setup code, the page shows its
  sign-in form only. Responses carry `Cache-Control: no-store`, `Referrer-Policy: no-referrer`
  and a Content Security Policy with `frame-ancestors 'none'`.
- Uploads with the admin token (`POST /v1/samples`, `GET /v1/status`), from an app set up by
  hand with the token of earlier versions (formerly the server token), are deprecated: this
  release accepts them, logs a warning at most hourly and counts them in
  `healthkit_relay_admin_token_uploads_total` on `/metrics`. Pair that iPhone again from the
  setup page. (App 0.4 and later swap a token typed in by hand for a paired device's token of
  their own on the first start.)

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

Required per sample: `uuid`, `type` (a HealthKit identifier, `HK[A-Za-z0-9]{1,120}`: it names
a directory of the archive), `start`, `end` (RFC 3339). Everything else is optional, and
unknown fields are archived as sent, so newer clients need no server change. A batch may
touch at most 2000 (type, month) partitions.

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

App 0.6.1 adds two optional top-level fields: `"trigger"`, what started the sync that sent
the batch (`foreground`: the app on screen; `observer`: HealthKit background delivery;
`refresh`: the hourly background app refresh; `unlock`: the first unlock after a sync was
skipped because the phone was locked; `retry`: a sync again after one failed), and
`"app_state"` (`active`, `inactive` or `background`, when the sync started). The server logs
both and counts batches by trigger; a batch without them (older apps) counts as `unknown`.
Neither is archived. Servers before 0.6.1 ignore them.

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
- `400` invalid JSON, a body cut short, or an invalid record. Nothing from the batch is
  stored.
- `401` missing, wrong or revoked token. `403` a read token. `413` body larger than
  `-max-body-bytes`. `500` the archive could not be written.

A batch is stored entirely or not at all, so a client can safely retry until it gets `200`.

The token is a paired device's token. The admin token is still accepted here and on
`GET /v1/status` in this release, deprecated (see [Setting up the app](#setting-up-the-app)).

Errors here and on every endpoint below are JSON: `{"error": "…"}`.

### `POST /v1/pair`

No token; the setup code is the credential. Body `{"code": "…", "name": "iPhone", "model":
"iPhone17,1", "replaces": "<the iPhone's previous token, optional>"}`. `200`
`{"token": "…", "device": {"id": "3f9a1c2e", "name": "iPhone", "model": "iPhone17,1",
"created_at": "…"}, "server": {…status…}}`; `403` for an unknown, used or expired setup code,
or a client outside `-setup-from`. The app shows the `403` message as is.

### `GET /v1/status`

A paired device's token (read tokens get `403`), or the admin token (deprecated).
`{"version": "…", "wire_version": 2, "records": 12571973, "device": {…}, "server_id":
"5f0c2a…", "preferred_url": "https://health.example.com"}` (`device` only for a paired
device's token; `preferred_url` only when `-public-url` is https). The app shows the record
count and detects a revoked token here, and moves to the preferred address (see
[Server address](#server-address)).

### `GET /v1/server`

No token. `{"server_id": "5f0c2a…", "wire_version": 2}`: the app checks that an address
reaches this server before it sends its token there.

### Server address

Each archive has a random server ID, made on first start in `<archive-dir>/server.json`; an
admin reset keeps it, and so does an archive moved to another machine. `GET /v1/status` and
the answer to each heartbeat (`POST /v1/heartbeat`, `200` with `{"server_id",
"preferred_url"}`) name it, with `-public-url` when that is https. When the app reaches the
server at another address (an IP address, an older name), it checks that the preferred
address answers `GET /v1/server` over HTTPS with the same ID, then `GET /v1/status` with its
token as the same device, and uses the preferred address from then on, with its history and
sync positions as they are. To move phones to a new address: serve the server there over
HTTPS, set `-public-url` to it, keep the old address working until the phones have synced
once (the server's log shows `heartbeat:` lines from each).

### `POST /v1/pairing-codes`, `GET /v1/devices`, `DELETE /v1/devices/{id}`

Admin token only: mint a setup code (`{"code", "expires_at", "link", "setup_url"}`, plus
`addresses` found when `-public-url` is not set; the body may give `{"public_url": "…",
"valid_seconds": 7200}`, by default 15 minutes, at most a day), list paired devices and read
tokens, revoke one (`204`, or `404` for an unknown ID). Each entry has a `scope`: `upload` (a
paired device's token, and every entry written before scopes existed) or `read`.

### `POST /v1/tokens`

Admin token only: `{"name": "grafana", "scope": "read"}` (scope defaults to `read`) answers
`201` with `{"token", "device"}`, the token shown this once. Read tokens may use the query API
and nothing else: uploads, status and erase answer `403`. `healthkit-relay token create -name
grafana` prints one; `devices` and `revoke` list and revoke them.

### Query API

With `-query-duckdb`: `GET /v1/types`, `/v1/types/{type}`,
`/v1/types/{type}/samples`, `/v1/types/{type}/aggregates`, for a read token or the admin
token (a paired device's token gets `403`). Described by `/openapi.json` (OpenAPI 3.1), shown
at `/docs`; both are public. Read tokens are also created on the setup page, under **Read
tokens** (`/setup#read-tokens`). See [docs/query-api.md](../docs/query-api.md).

### `DELETE /v1/data`

Erases, permanently, everything the caller uploaded: with a paired device's token, that
device's records; with the admin token, what was uploaded with the admin token, or with
`?device=<id>` what that device uploaded (also after it was revoked). It requires
`?confirm=erase`: any other value, or none, is refused (`400`, nothing erased). Only the app
before 0.6 may leave it out (a paired device's token, or iOS's CFNetwork user agent). `200`
`{"erased": 12571973, "device": "3f9a1c2e"}`. The app's **Erase Server Data and Start Over**
calls it, then uploads its history again from scratch. Numbers already forwarded to the
metrics database stay there. See [Erasing](#erasing).

### `GET /healthz`

`200 ok` while the process runs.

### `GET /metrics`

Private networks only, like the setup page (requests through Tailscale Funnel count as
public). Prometheus text format: `healthkit_relay_requests_total{result}` (`ok`, `unauthorized`,
`bad_request`, `incomplete` for a body cut short by a client going away, `archive_error`,
`paired`, `pair_rejected`, `erased`, `forbidden` for a token of the wrong scope,
`query_error`, `query_timeout`, `query_busy`, `demo_quota` for an upload refused by a demo
server's quota), `healthkit_relay_admin_token_uploads_total`,
`healthkit_relay_records_total{outcome}`,
`healthkit_relay_archive_records`, `healthkit_relay_staged_records` (not flushed yet),
`healthkit_relay_last_success_timestamp_seconds{job="flush"|"compaction"}` (alert when it
gets old: more than two hours for the flush, a day and a half for the compaction),
`healthkit_relay_maintenance_errors_total{job}`, `healthkit_relay_devices`,
`healthkit_relay_forward_errors_total`, `healthkit_relay_last_upload_timestamp_seconds`,
`healthkit_relay_batches_total{trigger}` (accepted batches by what started the app's sync,
see `trigger` above; `unknown` for apps before 0.6.1). `/metrics` holds counts and
timestamps only.

## Demo server

A demo server lets people try the app without running a server of their own, for example
App Review. Start it with `-demo-code-file` naming a file that holds a shared setup code, and
`-setup-from any` so that phones on the internet may pair:

```sh
printf 'ABCD-EFGH-JKLM-NPQR\n' > /etc/healthkit-relay-demo-code   # pick your own
healthkit-relay -archive-dir /var/lib/healthkit-relay-demo -public-url https://demo.example.com \
  -setup-from any -demo-code-file /etc/healthkit-relay-demo-code
```

- The shared code pairs any number of iPhones, each with a token of its own. In the app:
  **Enter Server Details Instead**, the server address and the code. Or open
  `https://demo.example.com/setup?code=ABCD-EFGH-JKLM-NPQR` on the iPhone and tap **Open in
  Health Relay**.
- Every `-demo-reset` (an hour by default) the server erases every record, and revokes
  devices unseen for a week.
- Between two erases it accepts `-demo-quota` bytes of uploads (256 MB by default); beyond
  that, uploads get `429` with `Retry-After`, and the app tries again later.

Run it with an archive of its own, never one holding real data.

## Archive

Everything lives in `-archive-dir`:

```
staging.db          SQLite: batches not flushed yet, the de-duplication index, tombstones
admin.json          the admin token's hash
devices.json        paired devices and read tokens (token hashes)
codes.json          setup codes until they expire (code hashes)
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
start, replacing what it wrote, so each row lands in Parquet exactly once. The tombstones are exported
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
`source_version`, `device`, `uploader` (the paired device's ID, `""` for uploads with the admin token),
`wire` (1 or 2), `received_at`, and `json`, the record exactly as sent, including fields
added by newer apps. In `records`, `type` is the record kind and `uuid` the
sample a chunk belongs to. The point datasets have one row per point: `sample_uuid`, `chunk`,
`i`, `time`, then `lat`, `lon`, `alt`, `h_acc`, `v_acc`, `speed`, `speed_acc`, `course`,
`course_acc`, `ellipsoidal_alt`, `floor` (routes); `t` (seconds since the sample's start) and
`gap` (heartbeats); `t` and `microvolts` (ECG, lead I); `start`, `end`, `value`, `unit`
(quantity series). A series chunk is also kept whole in `records` (type `series`).

**Deletions.** HealthKit tombstones go to the `deleted` table (and `deleted.parquet`, with
`wire` 0); a device's tombstones apply to what that device uploaded (each phone of an
iCloud-synced household sends its own). v1 copies superseded by v2 are listed there with
`wire` 1. The next nightly
compaction removes their rows from the Parquet files, with their series points: merged
partitions drop them on the way, and in the others only the UUID column of the files that
can hold them (their type's months) is read, and a file is rewritten only if it does. Until
then the views below hide them. HealthKit never reuses UUIDs; an edited sample arrives as a
deletion plus a new UUID.

### Querying with DuckDB

[`sql/views.sql`](sql/views.sql) defines views that read the Parquet files and hide deleted
rows: `samples`, `records`, `records_latest`, `route_points`, `heartbeats`, `ecg_voltages`,
`quantity_series`, `deleted`. Start DuckDB in the archive directory with it:

```sh
cd /var/lib/healthkit-relay && duckdb -init /path/to/server/sql/views.sql
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

`DELETE /v1/data` (see above) erases for good, at once: Parquet files holding only that
uploader's rows are removed, others are rewritten without them (a temp file renamed over the
original), its staged rows, tombstones and de-duplication entries are deleted (SQLite
overwrites deleted content, `secure_delete`), and the SQLite log is truncated. It is
journaled like flush and compaction: an erase a crash interrupts is finished on the next
start, before uploads are accepted. Uploads wait while it runs. Two copies stay: numbers
already forwarded to the metrics database (`-forward-url`; they age out with its retention),
and copies outside the archive directory (backups, filesystem snapshots). The token keeps
working, so the app can upload its history again at once; revoke the paired device to stop it.

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
activity summaries. Other kinds (routes, ECGs, correlations...) stay in the archive only. A
forwarding failure is logged and counted, and the upload still succeeds: the archive stays
the source of truth.

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
