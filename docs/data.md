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

<!-- storage: keep this section in step with the server's storage layer. -->

!!! info "Storage layout"
    This section describes the storage design of the current release. Run `DESCRIBE`
    (below) to see the exact columns your server writes.

Incoming batches land in a **SQLite staging database** in the data directory, which makes
each batch durable before the server answers and deduplicates on disk (by sample UUID, and
by record key and content), so memory use does not grow with history. Staged rows are
compacted into **Parquet** files, partitioned by data type and month:

```text
<data-dir>/
  parquet/
    type=HKQuantityTypeIdentifierHeartRate/
      month=2026-10/part-*.parquet
    type=HKWorkoutTypeIdentifier/
      month=2026-10/part-*.parquet
    ...
  staging.sqlite      # recent batches, before compaction
  devices.json        # paired devices (token hashes only)
```

Parquet is columnar and compressed, readable by DuckDB, pandas, Polars, Spark, ClickHouse and
most data tools, and stays readable without this project. **Erase server data** in the app
deletes all of it on the server.

## Querying with DuckDB

[DuckDB](https://duckdb.org) reads the Parquet files in place; no import, no server. Install
it (`brew install duckdb`, or the binary from duckdb.org), then from the data directory:

```sql
-- Everything, as one table, with type and month as columns from the folder names.
CREATE VIEW health AS
SELECT * FROM read_parquet('parquet/**/*.parquet', hive_partitioning = true, union_by_name = true);

DESCRIBE health;

-- What is there, and how much.
SELECT type, count(*) AS n, min(start) AS first, max(start) AS last
FROM health GROUP BY type ORDER BY n DESC;
```

Read one type only, so DuckDB opens only that folder:

```sql
-- Daily resting heart rate.
SELECT date_trunc('day', start) AS day, avg(value) AS resting_hr
FROM read_parquet('parquet/type=HKQuantityTypeIdentifierRestingHeartRate/*/*.parquet')
GROUP BY day ORDER BY day;

-- Heart rate variability (SDNN), weekly median, last year.
SELECT date_trunc('week', start) AS week, median(value) AS hrv_ms
FROM read_parquet('parquet/type=HKQuantityTypeIdentifierHeartRateVariabilitySDNN/*/*.parquet')
WHERE start > now() - INTERVAL 1 YEAR
GROUP BY week ORDER BY week;

-- Hours asleep per night, by stage.
SELECT date_trunc('day', "end") AS night, value_label AS stage,
       round(sum(epoch("end") - epoch(start)) / 3600, 2) AS hours
FROM read_parquet('parquet/type=HKCategoryTypeIdentifierSleepAnalysis/*/*.parquet')
WHERE value_label LIKE 'asleep%'
GROUP BY ALL ORDER BY night, stage;

-- Steps per day, counting each source once (iPhone and Watch both count steps).
SELECT date_trunc('day', start) AS day, source_name, sum(value) AS steps
FROM read_parquet('parquet/type=HKQuantityTypeIdentifierStepCount/*/*.parquet')
GROUP BY ALL ORDER BY day;
```

Export anything to CSV or another Parquet file:

```sql
COPY (SELECT * FROM health WHERE type = 'HKWorkoutTypeIdentifier')
TO 'workouts.csv' (HEADER);
```

From Python:

```python
import duckdb
con = duckdb.connect()
df = con.sql("""
    SELECT start, value FROM read_parquet(
      'parquet/type=HKQuantityTypeIdentifierHeartRate/*/*.parquet')
""").df()
```

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
