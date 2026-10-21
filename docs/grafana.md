# Grafana

[Grafana](https://grafana.com/oss/grafana/) draws dashboards: charts of your heart rate,
steps, sleep and workouts over time. It reads them from a time-series database, a database
for numbers over time. The server fills that database: with `-forward-url` set, it sends each
new number it stores, such as a heart rate reading, to the database as well.

[VictoriaMetrics](https://victoriametrics.com) is the easiest database to pair with Grafana;
InfluxDB works too. Grafana shows the numbers. For the rest of the archive (routes, ECGs,
sleep sessions as rows), use [DuckDB](data.md#querying-with-duckdb) or the
[query API](query-api.md).

## With Docker Compose

The Compose file has VictoriaMetrics and Grafana ready, with Grafana already connected to
VictoriaMetrics. Run these in the folder with `docker-compose.yml`:

1. Tell the server to forward numbers to VictoriaMetrics:

    ```sh
    echo HKR_FORWARD_URL=http://victoriametrics:8428/influx/write >> .env
    ```

2. Start the server, VictoriaMetrics and Grafana:

    ```sh
    docker compose --profile metrics up -d
    ```

    `docker compose --profile metrics ps` lists `relay`, `victoriametrics` and `grafana`, each
    with a status of `Up`.

3. Check that the server forwards:

    ```sh
    docker compose logs relay | grep forwarding
    ```

    It shows `forwarding numeric samples to http://victoriametrics:8428/influx/write`.

4. In the app, tap **Sync Now**. New data goes to VictoriaMetrics as it arrives.
5. Open Grafana on port 3000 of the server, e.g. `http://192.168.1.10:3000`. Sign in with
   the user `admin` and the password `admin`; Grafana then asks you to choose a new password.
6. Open **Explore** from the menu. The data source is **VictoriaMetrics**. Type
   `healthkit_heart_rate` in the query box and click **Run query**. A chart of your heart
   rate appears.

VictoriaMetrics keeps data for 100 years in this Compose file (`-retentionPeriod=100y`).

The server forwards the numbers it stores from now on. To send the history it already holds,
open the app's **Advanced** screen (the gear) and tap **Erase Server Data and Start Over**:
the iPhone uploads its whole history again, and the server forwards all of it. See
[Erasing](data.md#erasing) for what that deletes first.

## With VictoriaMetrics on your server

1. Install and start VictoriaMetrics, with a long retention, e.g.
   `victoria-metrics -retentionPeriod=100y`. Its default keeps one month, and the first
   sync sends your entire history, so a month would drop most of it. On NixOS, see
   [VictoriaMetrics](setup/nixos.md#victoriametrics).
2. Start the server with:

    ```sh
    -forward-url http://127.0.0.1:8428/influx/write
    ```

    (`HKR_FORWARD_URL=http://127.0.0.1:8428/influx/write` in `/etc/healthkit-relay/env`, or
    `forwardUrl` on NixOS). The server's log shows `forwarding numeric samples to ...`.

3. In Grafana, open **Connections** › **Data sources**, add a **Prometheus** data source
   with the URL `http://127.0.0.1:8428` (VictoriaMetrics answers Prometheus queries), and
   click **Save & test**. Grafana reports that it queried the data source successfully.
4. Open **Explore**, pick that data source, and run `healthkit_heart_rate`.

The archive keeps everything, whatever the retention of the time-series database.

## Queries

VictoriaMetrics names each series `<measurement>_<field>`, so heart rate becomes
`healthkit_heart_rate`, with the labels `source` (e.g. `Apple Watch`) and `unit` (e.g.
`count/min`). Field names are listed in [Metrics forwarding](data.md#metrics-forwarding).
Example queries, in PromQL (the Prometheus query language, which VictoriaMetrics's MetricsQL
extends):

| Panel | PromQL / MetricsQL |
|---|---|
| Heart rate | `healthkit_heart_rate` |
| Resting heart rate, daily | `avg_over_time(healthkit_resting_heart_rate[1d])` |
| HRV (SDNN), 7-day average | `avg_over_time(healthkit_heart_rate_variability_sdnn[7d])` |
| Steps per day, Apple Watch only | `sum_over_time(healthkit_step_count{source=~".*Watch.*"}[1d])` |
| Sleep, hours per night | `sum by (value) (sum_over_time(healthkit_sleep_analysis_seconds{value=~"asleep.*"}[1d])) / 3600` |
| VO2 max | `last_over_time(healthkit_vo2_max[30d])` |
| Workout minutes per week | `sum_over_time(healthkit_workout_duration_seconds[7d]) / 60` |

Health samples come at irregular times (heart rate every few minutes, VO2 max every few
days). Use the `*_over_time` functions with a window, and set the panel's *Min interval* to
`1d` for daily values.

## InfluxDB

InfluxDB 2:

```sh
-forward-url 'http://influxdb:8086/api/v2/write?org=home&bucket=health&precision=ns' \
-forward-token-file /path/to/influx-token
```

InfluxDB 1.x: `-forward-url 'http://influxdb:8086/write?db=health'`. Query the measurement
`healthkit`, the field `heart_rate`, and so on.

## Prometheus

Prometheus collects metrics by reading them from other programs, while the server sends
them. Put VictoriaMetrics next to Prometheus (and add it to Grafana as a second Prometheus
data source), or receive the lines with Telegraf's `influxdb_listener` input and expose them
with its `prometheus_client` output.

## Monitoring the server itself

`/metrics` gives Prometheus metrics about the server itself: counts and timestamps. `healthkit_relay_requests_total{result}`, `healthkit_relay_records_total{outcome}`,
`healthkit_relay_archive_records`, `healthkit_relay_staged_records`,
`healthkit_relay_last_success_timestamp_seconds{job="flush"|"compaction"}`,
`healthkit_relay_maintenance_errors_total{job}`, `healthkit_relay_devices`,
`healthkit_relay_forward_errors_total`, `healthkit_relay_admin_token_uploads_total`,
`healthkit_relay_last_upload_timestamp_seconds` and `healthkit_relay_batches_total{trigger}`
(batches by what started the sync: `foreground`, `observer`, `refresh`, `unlock`, `retry`,
or `unknown` for apps before 0.6.1). Collect them with VictoriaMetrics or Prometheus.

Useful alerts:

- the hourly flush has not succeeded for two hours, or the nightly compaction for a day and
  a half:

    ```promql
    time() - healthkit_relay_last_success_timestamp_seconds{job="flush"} > 7200
    time() - healthkit_relay_last_success_timestamp_seconds{job="compaction"} > 129600
    ```

- no upload for a day, which usually means the phone could not reach the server, or
  background syncing is paused:

    ```promql
    time() - healthkit_relay_last_upload_timestamp_seconds > 86400
    ```

Whether the phone syncs in the background at all shows in:

```promql
sum by (trigger) (increase(healthkit_relay_batches_total{trigger!~"foreground|unknown"}[1d]))
```
