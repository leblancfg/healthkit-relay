# Grafana

Grafana charts the numbers the server forwards to a time-series database (`-forward-url`):
VictoriaMetrics is the simplest, InfluxDB works too. Grafana does not read the Parquet
archive; for what is not a number over time (routes, ECGs, sleep sessions as rows), query the
archive with [DuckDB](data.md#querying-with-duckdb).

## VictoriaMetrics (Prometheus-compatible)

Run the server with:

```sh
-forward-url http://127.0.0.1:8428/influx/write
```

VictoriaMetrics names each series `<measurement>_<field>`, so heart rate becomes
`healthkit_heart_rate` with labels `source` and `unit`. Give VictoriaMetrics a long retention
(`-retentionPeriod=100y`): the first sync sends your entire history, and the default
retention (one month) would drop it. The Parquet archive keeps everything regardless.

In Grafana, add a **Prometheus** data source pointing at `http://<victoriametrics>:8428`.
Example queries:

| Panel | PromQL / MetricsQL |
|---|---|
| Heart rate | `healthkit_heart_rate` |
| Resting heart rate, daily | `avg_over_time(healthkit_resting_heart_rate[1d])` |
| HRV (SDNN), 7-day average | `avg_over_time(healthkit_heart_rate_variability_sdnn[7d])` |
| Steps per day, Apple Watch only | `sum_over_time(healthkit_step_count{source=~".*Watch.*"}[1d])` |
| Sleep, hours per night | `sum by (value) (sum_over_time(healthkit_sleep_analysis_seconds{value=~"asleep.*"}[1d])) / 3600` |
| VO2 max | `last_over_time(healthkit_vo2_max[30d])` |
| Workout minutes per week | `sum_over_time(healthkit_workout_duration_seconds[7d]) / 60` |

Health samples are irregular (heart rate every few minutes, VO2 max every few days), so use
`*_over_time` functions with a window, and set the panel's *Min interval* to `1d` for daily
values.

## InfluxDB

InfluxDB 2:

```sh
-forward-url 'http://influxdb:8086/api/v2/write?org=home&bucket=health&precision=ns' \
-forward-token-file /path/to/influx-token
```

InfluxDB 1.x: `-forward-url 'http://influxdb:8086/write?db=health'`. Query measurement
`healthkit`, field `heart_rate`, and so on.

## Prometheus

Prometheus does not accept pushed line protocol. Put VictoriaMetrics next to it (it can be
added to Grafana as a second Prometheus data source), or receive the lines with Telegraf's
`influxdb_listener` input and expose them with its `prometheus_client` output.

## Monitoring the server itself

`/metrics` exposes Prometheus metrics about the server, never health data:
`healthkit_relay_requests_total{result}`, `healthkit_relay_records_total{outcome}`,
`healthkit_relay_archive_records`, `healthkit_relay_staged_records`,
`healthkit_relay_last_success_timestamp_seconds{job="flush"|"compaction"}`,
`healthkit_relay_maintenance_errors_total{job}`, `healthkit_relay_devices`,
`healthkit_relay_forward_errors_total` and `healthkit_relay_last_upload_timestamp_seconds`.
Scrape it with VictoriaMetrics (or Prometheus). Useful alerts: the hourly flush has not
succeeded for two hours, or the nightly compaction for a day and a half,

```promql
time() - healthkit_relay_last_success_timestamp_seconds{job="flush"} > 7200
time() - healthkit_relay_last_success_timestamp_seconds{job="compaction"} > 129600
```

and no upload for a day,

```promql
time() - healthkit_relay_last_upload_timestamp_seconds > 86400
```

which usually means the phone could not reach the server, or background syncing is paused.
