# Data model & querying

## What is synced

Everything HealthKit exposes on the phone. The app's type list is generated from the iOS
SDK it is built with, so a newer Xcode brings new types with no code change. With the
iOS 27 SDK that is over 200 types:

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
  prescriptions**, **Health Records** (FHIR) and **CDA documents**;
- daily **activity summaries** (rings and goals) and **characteristics** (date of birth,
  biological sex, blood type...).

Every sample keeps its UUID, start and end, source (app and version), device and metadata,
exactly as sent. Most kinds also carry Apple's own serialized object (`NSKeyedArchiver`), so
properties without a dedicated field are still kept. Deletions in Apple Health are synced as
deletions.

The wire format is documented in the
[server README](https://github.com/leblancfg/healthkit-relay/blob/main/server/README.md#api).

## Storage

<!-- storage: keep this section in step with the server's storage layer (server/README.md, Archive). -->

Everything lives in the archive directory (`-archive-dir`, `HKR_ARCHIVE_DIR`):

```text
<archive-dir>/
  staging.db                 SQLite: batches not flushed yet, the de-duplication index, tombstones
  devices.json               paired devices (token hashes only)
  parquet/
    samples/type=HKQuantityTypeIdentifierHeartRate/month=2026-10/c1234.parquet
    samples/type=.../month=.../f5678.parquet     an hourly flush, not compacted yet
    records/type=activity_summary/month=2026-10/...
    route_points/type=HKWorkoutRouteTypeIdentifier/month=.../...
    heartbeats/type=HKDataTypeIdentifierHeartbeatSeries/month=.../...
    ecg_voltages/type=HKDataTypeIdentifierElectrocardiogram/month=.../...
    quantity_series/type=HKQuantityTypeIdentifierStepCount/month=.../...
    */type=_/month=_/schema.parquet              no rows: queries work before any data arrives
    deleted/deleted.parquet                      tombstones
```

- **Write path.** A batch is validated whole and committed to `staging.db` (fsynced) before
  the app gets its `200`. De-duplication is an on-disk index of every record ever stored, so
  memory does not grow with history: re-sending a batch stores nothing twice.
- **Flush, hourly** (`-flush-interval`). Staged rows are streamed into a new Parquet file per
  dataset, type and month, `f<seq>.parquet`, then removed from staging.
- **Compaction, nightly** (`-compact-at`, 03:30 local time by default). Each partition with
  more than one file is merged into one, `c<seq>.parquet`, and samples deleted in Apple
  Health are dropped from the files on the way. Months that received nothing are not
  rewritten.
- **Partitions.** `month` is the month of the sample's start, in UTC (`YYYY-MM`). `type` is the
  HealthKit type identifier, or the record kind in `records`.

Flush, compaction and erase are journaled and atomic (temp file, fsync, rename): a crash
loses nothing and duplicates nothing. DuckDB can read the files while the server runs.

### Columns

`samples` and `records`: `uuid`, `type`, `kind`, `key` (records), `start`, `end`
(timestamps, UTC), `value` (double), `unit`, `source_name`, `source_bundle`,
`source_version`, `device`, `uploader` (the paired device's ID, empty for uploads with the admin token),
`wire` (wire format, 1 or 2), `received_at`, and `json`, the record exactly as the app sent
it, so fields without a column (metadata, category value names, Apple's serialized object)
are kept. In `records`, `type` is the record kind (`activity_summary`, `characteristics`,
`medication`, `series`...) and `uuid` the sample a chunk belongs to.

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

Parquet is columnar and compressed, readable by DuckDB, pandas, Polars, Spark, ClickHouse and
most data tools, and stays readable without this project.

### Erasing

**Erase Server Data and Start Over** in the app calls `DELETE /v1/data?confirm=erase`, which
permanently deletes everything that iPhone uploaded: Parquet files are rewritten without its
rows (or removed), its staged rows, tombstones and de-duplication entries are deleted, and
SQLite's log is truncated. There is no soft delete. The app then uploads its history again.
Not erased: numbers already forwarded to the metrics database (they age out with its
retention) and copies outside the archive directory, such as backups and snapshots. With the
admin token, `?confirm=erase` is required; `?device=<id>` erases one device's data.

## Querying with DuckDB

[DuckDB](https://duckdb.org) reads the Parquet files in place; no import, no server. Install
it (`brew install duckdb`, or the binary from duckdb.org), download
[`server/sql/views.sql`](https://github.com/leblancfg/healthkit-relay/blob/main/server/sql/views.sql),
and start DuckDB in the archive directory with it:

```sh
cd /var/lib/healthkit-relay && duckdb -init /path/to/views.sql
```

It defines the views `samples`, `records`, `records_latest` (the latest version of a record
that changes, such as today's activity summary), `route_points`, `heartbeats`,
`ecg_voltages`, `quantity_series` and `deleted`. They hide what was deleted in Apple Health
but not compacted away yet. Filtering on `type` and `month` reads only the matching files.

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

Records received in the last hour may still be in `staging.db` only; `healthkit-relay flush`
(with the server stopped) writes them out at once.

Export anything to CSV or another Parquet file:

```sql
COPY (SELECT * FROM samples WHERE type = 'HKWorkoutTypeIdentifier')
TO 'workouts.csv' (HEADER);
```

From Python:

```python
import duckdb
con = duckdb.connect()
con.execute(open("/path/to/views.sql").read())
df = con.sql("""
    SELECT start, value FROM samples
    WHERE type = 'HKQuantityTypeIdentifierHeartRate'
""").df()
```

Run it from the archive directory: the views use paths relative to it.

!!! tip "Sum versus average"
    Steps, distance and energy are additive: sum them, but de-duplicate overlapping sources
    (iPhone and Apple Watch both count steps), for instance by keeping one `source_name`. Heart
    rate, HRV and similar are discrete measurements: average them.

## Metrics forwarding

If `-forward-url` is set, new numeric samples are also posted as InfluxDB line protocol after
they are stored:

| Sample | Line |
|---|---|
| quantity | `healthkit,source=…,unit=count/min heart_rate=62 <start>` |
| category | `healthkit,source=…,value=asleepDeep sleep_analysis_seconds=1800 <start>` (duration) |
| workout | `healthkit,activity=running,source=… workout_duration_seconds=…`, `workout_energy_kcal`, `workout_distance_meters` |
| activity summary | `healthkit,unit=kcal activity_summary_active_energy_burned=512 <day start>` |

Field names are the HealthKit identifier in snake case
(`HKQuantityTypeIdentifierHeartRateVariabilitySDNN` → `heart_rate_variability_sdnn`). Routes,
ECGs, documents and other non-numeric data stay in the archive. A forwarding failure is
logged and counted but never fails an upload. See [Grafana](grafana.md).
