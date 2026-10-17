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
| `-archive-dir` | (required) | archive directory, created if missing |
| `-token-file` | (required) | file holding the bearer token (at least 16 characters; surrounding whitespace ignored) |
| `-forward-url` | | line-protocol write URL, e.g. `http://127.0.0.1:8428/influx/write` (VictoriaMetrics) or `http://influx:8086/api/v2/write?org=o&bucket=b&precision=ns` (InfluxDB 2) |
| `-forward-token-file` | | bearer token for `-forward-url` |
| `-measurement` | `healthkit` | line-protocol measurement name |
| `-max-body-bytes` | `33554432` | largest accepted request |

The token is read from a file, never from a flag value, so it stays out of process
listings. With systemd, `LoadCredential=` and `-token-file ${CREDENTIALS_DIRECTORY}/token`
work well.

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

Responses:

- `200` `{"received": 4, "stored": 3, "duplicates": 1, "deletions": 1}`: the batch is on disk.
- `400` invalid JSON or an invalid record. Nothing from the batch is stored.
- `401` missing or wrong token. `413` body too large. `500` the archive could not be written.

A batch is stored entirely or not at all, so a client can safely retry until it gets 200.

### `GET /healthz`

`200 ok` while the process runs.

### `GET /metrics`

Prometheus text format: `healthkit_relay_requests_total{result}`,
`healthkit_relay_records_total{outcome}`, `healthkit_relay_archive_records`,
`healthkit_relay_forward_errors_total`, `healthkit_relay_last_upload_timestamp_seconds`.
No health data.

## Archive

`<archive-dir>/YYYY/YYYY-MM-DD.jsonl`, one file per UTC day of receipt, one record per line:

```json
{"op":"sample","uuid":"1B4E28BA-…","type":"HKQuantityTypeIdentifierHeartRate","received_at":"2026-10-17T16:00:00Z","data":{…sample as sent…}}
{"op":"delete","uuid":"3B4E28BA-…","type":"HKQuantityTypeIdentifierStepCount","received_at":"…","data":{…}}
```

- Files are only appended to, and each write is fsynced before the response.
- Each `(op, uuid)` is stored once: re-sent samples and tombstones are dropped. On start
  the server reads the archive to rebuild this index (memory grows with record count:
  roughly 100 bytes per record).
- A sample is current unless a `delete` record with the same `uuid` exists, in any file.
  HealthKit never reuses UUIDs; edited samples arrive as a deletion plus a new UUID.
- A line cut short by a crash is skipped on the next start; the client re-sends it.

Example, every heart-rate value received:

```sh
cat archive/*/*.jsonl | jq -r 'select(.op=="sample" and .type=="HKQuantityTypeIdentifierHeartRate") | [.data.start, .data.value] | @tsv'
```

## Metrics forwarding

New samples (not duplicates) that carry a number are posted as line protocol after they
are archived. A forwarding failure is logged and counted but does not fail the upload:
the archive stays the source of truth.

| Sample | Line |
|--------|------|
| quantity | `healthkit,source=…,unit=count/min heart_rate=62 <start ns>` |
| category | `healthkit,source=…,value=asleepDeep sleep_analysis_seconds=1800 <start ns>` (duration) |
| workout | `healthkit,activity=running,source=… workout_duration_seconds=…`, `workout_energy_kcal`, `workout_distance_meters` |

Field names are the HealthKit identifier in snake case
(`HKQuantityTypeIdentifierHeartRateVariabilitySDNN` → `heart_rate_variability_sdnn`).
VictoriaMetrics names the series `<measurement>_<field>`, e.g. `healthkit_heart_rate`.
Note that a metrics database with a short retention keeps only recent data from a full
history upload; the archive keeps everything.
