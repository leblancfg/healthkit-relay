# Data model & querying

The server keeps your data in the **archive**: the folder given by `-archive-dir`
(`HKR_ARCHIVE_DIR`), e.g. `/var/lib/healthkit-relay`, or `./data` next to the Docker Compose
file. This page says what the archive holds, how it is laid out, and how to read it with
[DuckDB](https://duckdb.org). For a program that reads over the network, such as a
dashboard, see the [query API](query-api.md). Words such as *paired device* and *admin token*
are defined in the [glossary](glossary.md).

## What is synced

The app reads every type of data HealthKit offers on the phone. Its type list is generated
from the iOS SDK it is built with: an app built with a newer Xcode reads the new types too.
With the iOS 27 SDK that is over 200 types:

- **Quantities** (heart rate, HRV, resting heart rate, walking heart rate, VO2 max, steps,
  distance, active and basal energy, blood oxygen, respiratory rate, wrist temperature,
  body mass, nutrition, audio exposure, mobility...), each with one unit for its whole
  history and the individual values of series samples;
- **Categories** (sleep stages, symptoms, cycle tracking, mindful minutes, high/low heart
  rate and irregular rhythm events...), with value names;
- **Workouts** with activities, events, per-type statistics and **routes** (every GPS
  point);
- **Heartbeat series**, **ECGs** (every voltage sample, classification), **audiograms**;
- **Correlations** (blood pressure, food) with their members;
- **State of Mind**, **GAD-7 / PHQ-9**, **medications** and dose events, **vision
  prescriptions**; **Health Records** (FHIR) and **CDA documents** in builds of your own
  with Health Records turned on (the App Store app leaves clinical records out);
- daily **activity summaries** (rings and goals) and **characteristics** (date of birth,
  biological sex, blood type...).

Every sample keeps its UUID, start and end, source (app and version), device and metadata,
exactly as sent. Most kinds also carry Apple's own serialized object (`NSKeyedArchiver`),
which keeps the properties that have no field of their own. A sample you delete in Apple
Health is deleted in the archive too.

The format the app sends is documented in the
[server README](https://github.com/leblancfg/healthkit-relay/blob/main/server/README.md#api).

## Storage

<!-- storage: keep this section in step with the server's storage layer (server/README.md, Archive). -->

Everything lives in the archive directory:

```text
<archive-dir>/
  staging.db                 SQLite: batches not flushed yet, the de-duplication index, tombstones
  admin.json                 the admin token's SHA-256
  devices.json               paired devices and read tokens (SHA-256 of each token)
  codes.json                 setup codes not used yet (SHA-256 of each code)
  parquet/
    samples/type=HKQuantityTypeIdentifierHeartRate/month=2026-10/c1234.parquet
    samples/type=.../month=.../f5678.parquet     an hourly flush, not compacted yet
    records/type=activity_summary/month=2026-10/...
    route_points/type=HKWorkoutRouteTypeIdentifier/month=.../...
    heartbeats/type=HKDataTypeIdentifierHeartbeatSeries/month=.../...
    ecg_voltages/type=HKDataTypeIdentifierElectrocardiogram/month=.../...
    quantity_series/type=HKQuantityTypeIdentifierStepCount/month=.../...
    */type=_/month=_/schema.parquet              zero rows: queries work before any data arrives
    deleted/deleted.parquet                      tombstones
```

- **Write path.** The server checks a whole batch, then commits it to `staging.db` (written
  to disk with fsync) before it answers the app with `200`. De-duplication uses an index on
  disk of every record ever stored, so memory use stays the same as history grows, and a
  batch sent twice is stored once.
- **Flush, hourly** (`-flush-interval`). Staged rows are written into a new Parquet file per
  dataset, type and month, `f<seq>.parquet`, then removed from staging.
- **Compaction, nightly** (`-compact-at`, 03:30 local time by default). Each partition with
  more than one file is merged into one, `c<seq>.parquet`, and samples deleted in Apple
  Health are dropped from the files on the way. Months that received nothing keep their
  files as they are.
- **Partitions.** `month` is the month of the sample's start, in UTC (`YYYY-MM`). `type` is the
  HealthKit type identifier, or the record kind in `records`.

Flush, compaction and erase are journaled and atomic (temp file, fsync, rename): after a
crash, every record is there once. DuckDB can read the files while the server runs.

### Columns

`samples` and `records`: `uuid`, `type`, `kind`, `key` (records), `start`, `end`
(timestamps, UTC), `value` (double), `unit`, `source_name`, `source_bundle`,
`source_version`, `device`, `uploader` (the paired device's ID; empty for uploads with the
admin token), `wire` (wire format, 1 or 2), `received_at`, and `json`, the record exactly as
the app sent it, with the fields that have no column (metadata, category value names, Apple's
serialized object). In `records`, `type` is the record kind (`activity_summary`,
`characteristics`, `medication`, `series`...) and `uuid` the sample a chunk belongs to.

The series datasets have one row per point, joined to `samples.uuid` by `sample_uuid`, and
ordered by `chunk` then `i`:

| Dataset | Columns |
|---|---|
| `route_points` | `sample_uuid`, `chunk`, `i`, `time`, `lat`, `lon`, `alt`, `h_acc`, `v_acc`, `speed`, `speed_acc`, `course`, `course_acc`, `ellipsoidal_alt`, `floor` |
| `heartbeats` | `sample_uuid`, `chunk`, `i`, `time`, `t` (seconds since the sample's start), `gap` |
| `ecg_voltages` | `sample_uuid`, `chunk`, `i`, `time`, `t`, `microvolts` (lead I) |
| `quantity_series` | `sample_uuid`, `chunk`, `i`, `start`, `end`, `value`, `unit` |
| `deleted` | `uuid`, `wire`, `type`, `reason`, `uploader`, `received_at`, `json` |

Every point dataset also has `uploader` and `received_at`; with `hive_partitioning`, DuckDB
adds `type` and `month` from the folder names.

Parquet is a compressed file format for tables. DuckDB, pandas, Polars, Spark, ClickHouse and
most data tools read it, with or without Health Relay.

### Erasing

**Erase Server Data and Start Over**, in the app's **Advanced** screen (the gear), calls
`DELETE /v1/data?confirm=erase`. The server permanently deletes everything that iPhone
uploaded: it rewrites the Parquet files without its rows (or removes them), deletes its
staged rows, tombstones and de-duplication entries, and truncates SQLite's log. The app then
uploads its history again. Two copies stay as they are: numbers already forwarded to a
metrics database (they age out with its retention), and copies outside the archive
directory, such as backups and snapshots. With the admin token, `?confirm=erase` is required,
and `?device=<id>` erases one paired device's data.

## Querying with DuckDB

[DuckDB](https://duckdb.org) is a database program that reads the Parquet files where they
are. You run it on the computer that holds the archive.

1. Install DuckDB: `brew install duckdb` on macOS, or download the command-line program from
   [duckdb.org](https://duckdb.org/docs/installation/). Check it with `duckdb --version`: it
   prints a version such as `v1.5.0`.
2. Download the views, the table definitions for the archive:

    ```sh
    curl -LO https://raw.githubusercontent.com/leblancfg/healthkit-relay/main/server/sql/views.sql
    ```

    This saves [`views.sql`](https://github.com/leblancfg/healthkit-relay/blob/main/server/sql/views.sql)
    in the current folder.

3. Go to the archive directory and start DuckDB with the views. On Linux and NixOS the
   archive belongs to the server's user, so open a root shell first (replace `/home/you`
   with the folder you downloaded `views.sql` to):

    ```sh
    sudo -s
    cd /var/lib/healthkit-relay
    duckdb -init /home/you/views.sql
    ```

    With Docker Compose and on macOS, the archive is yours: `cd` to it (e.g. `cd data` next
    to `docker-compose.yml`) and run `duckdb -init ~/views.sql`. DuckDB shows its prompt,
    `D`.

4. Type a query and press Enter:

    ```sql
    SELECT type, count(*) AS n FROM samples GROUP BY type ORDER BY n DESC LIMIT 3;
    ```

    DuckDB prints a table:

    ```text
    ┌────────────────────────────────────────────┬─────────┐
    │                    type                    │    n    │
    │                  varchar                   │  int64  │
    ├────────────────────────────────────────────┼─────────┤
    │ HKQuantityTypeIdentifierHeartRate          │ 1204311 │
    │ HKQuantityTypeIdentifierStepCount          │  402118 │
    │ HKQuantityTypeIdentifierActiveEnergyBurned │  398765 │
    └────────────────────────────────────────────┴─────────┘
    ```

5. Type `.quit` to leave DuckDB (then `exit` to leave the root shell).

The views are `samples`, `records`, `records_latest` (the latest version of a record that
changes, such as today's activity summary), `route_points`, `heartbeats`, `ecg_voltages`,
`quantity_series` and `deleted`. They hide what was deleted in Apple Health and is still in
the files until the nightly compaction. A query that filters on `type` and `month` reads only
the matching files.

```sql
DESCRIBE samples;

-- What is there, and how much.
SELECT type, count(*) AS n, min(start) AS first, max(start) AS last
FROM samples GROUP BY type ORDER BY n DESC;

-- Daily resting heart rate.
SELECT date_trunc('day', timezone('America/New_York', start)) AS day, avg(value) AS resting_hr
FROM samples WHERE type = 'HKQuantityTypeIdentifierRestingHeartRate'
GROUP BY day ORDER BY day;

-- Heart rate variability (SDNN), weekly median, last year.
SELECT date_trunc('week', start) AS week, median(value) AS hrv_ms
FROM samples WHERE type = 'HKQuantityTypeIdentifierHeartRateVariabilitySDNN'
  AND start > now() - INTERVAL 1 YEAR
GROUP BY week ORDER BY week;

-- Hours asleep per night, by stage (the stage name is in the sample as sent).
SELECT date_trunc('day', timezone('America/New_York', "end")) AS night,
       json_extract_string(json, '$.value_label') AS stage,
       round(sum(epoch("end") - epoch(start)) / 3600, 2) AS hours
FROM samples WHERE type = 'HKCategoryTypeIdentifierSleepAnalysis'
GROUP BY ALL ORDER BY night, stage;

-- Steps per day and source (iPhone and Watch both count steps).
SELECT date_trunc('day', start) AS day, source_name, sum(value) AS steps
FROM samples WHERE type = 'HKQuantityTypeIdentifierStepCount'
GROUP BY ALL ORDER BY day;

-- A workout route.
SELECT time, lat, lon, alt, speed FROM route_points
WHERE sample_uuid = '<route uuid>' ORDER BY chunk, i;

-- Activity rings, latest version of each day.
SELECT json_extract_string(json, '$.date') AS day,
       json_extract(json, '$.values.active_energy_burned.value')::double AS kcal
FROM records_latest WHERE type = 'activity_summary' ORDER BY day;
```

Records received in the last hour may still be in `staging.db` only. To write them to Parquet
at once, stop the server and run `healthkit-relay flush` with its `-archive-dir`.

Export anything to CSV or another Parquet file:

```sql
COPY (SELECT * FROM samples WHERE type = 'HKWorkoutTypeIdentifier')
TO 'workouts.csv' (HEADER);
```

From Python, started in the archive directory (the views use paths relative to it):

```python
import duckdb
con = duckdb.connect()
con.execute(open("/path/to/views.sql").read())
df = con.sql("""
    SELECT start, value FROM samples
    WHERE type = 'HKQuantityTypeIdentifierHeartRate'
""").df()
```

!!! tip "Sum versus average"
    Steps, distance and energy add up: sum them. An iPhone and an Apple Watch carried
    together both count the same steps, so keep one source per moment, for instance one
    `source_name`. The [query API's aggregates](query-api.md#counting-each-moment-once-dedup)
    do this the way Apple Health does. Heart rate, HRV and similar are single measurements:
    average them.

## Metrics forwarding

With `-forward-url` set, the server also posts each new numeric sample to a metrics database
as InfluxDB line protocol, after it is stored:

| Sample | Line |
|---|---|
| quantity | `healthkit,source=…,unit=count/min heart_rate=62 <start>` |
| category | `healthkit,source=…,value=asleepDeep sleep_analysis_seconds=1800 <start>` (duration) |
| workout | `healthkit,activity=running,source=… workout_duration_seconds=…`, `workout_energy_kcal`, `workout_distance_meters` |
| activity summary | `healthkit,unit=kcal activity_summary_active_energy_burned=512 <day start>` |

Field names are the HealthKit identifier in snake case
(`HKQuantityTypeIdentifierHeartRateVariabilitySDNN` → `heart_rate_variability_sdnn`). Routes,
ECGs, documents and the other data without a number stay in the archive only. When
forwarding fails, the server logs it and counts it in `healthkit_relay_forward_errors_total`;
the upload still succeeds. See [Grafana](grafana.md).
