---
title: FAQ
seo_title: "FAQ: export Apple Health data to your own server - Health Relay"
description: >-
  Answers about Health Relay: exporting Apple Health data to your own server, self-hosted
  HealthKit sync, Apple Health to Grafana, InfluxDB, VictoriaMetrics and DuckDB, and how it
  compares with Health Auto Export.
---

# FAQ

## How do I export Apple Health data to my own server?

Run the Health Relay server on a computer you own, then pair the Health Relay iPhone app with
it once:

1. Start the server, e.g. with Docker:

    ```sh
    mkdir -p data
    docker run -d --name healthkit-relay --restart unless-stopped \
      -p 8080:8080 --user "$(id -u):$(id -g)" \
      -e HKR_PUBLIC_URL=http://192.168.1.10:8080 \
      -v "$PWD/data:/data" \
      ghcr.io/leblancfg/healthkit-relay:latest
    ```

    `docker logs healthkit-relay` shows the admin token.

2. Open the server's setup page in a browser, e.g. `http://192.168.1.10:8080/setup`, sign in
   with the admin token, and scan the QR code with the iPhone camera.
3. Allow Health access in the app.

The app uploads your whole Apple Health history first, then new data several times a day.
The [quick start](quickstart.md) walks through each step with what you should see.

## Is there a self-hosted Apple Health sync?

Health Relay is one: the iPhone app sends HealthKit data only to the server address you pair
it with, a server you run at home, on a NAS or on a rented machine. The server is one Go
program, published as a Docker image and as binaries for Linux, macOS and FreeBSD, with
[setup guides](setup/index.md) for Docker, systemd, NixOS, macOS, Synology, Unraid, TrueNAS
and Raspberry Pi.

## How do I get HealthKit data into Grafana?

Have the server forward numbers to a time-series database, and point Grafana at that
database. With the Docker Compose file, VictoriaMetrics and Grafana come preconfigured:

```sh
echo HKR_FORWARD_URL=http://victoriametrics:8428/influx/write >> .env
docker compose --profile metrics up -d
```

In Grafana, `healthkit_heart_rate` charts your heart rate. The [Grafana page](grafana.md)
covers dashboards, queries and setups without Compose.

## Can I send Apple Health data to InfluxDB or VictoriaMetrics?

Yes. The server forwards every numeric sample (heart rate, HRV, steps, sleep durations,
VO2 max...) in InfluxDB line protocol to the URL in `-forward-url`:

- VictoriaMetrics: `-forward-url http://127.0.0.1:8428/influx/write`
- InfluxDB 2: `-forward-url 'http://influxdb:8086/api/v2/write?org=home&bucket=health&precision=ns'`
  with `-forward-token-file`
- InfluxDB 1.x: `-forward-url 'http://influxdb:8086/write?db=health'`
- Prometheus: through Telegraf's HTTP listener, which accepts line protocol

See [Grafana](grafana.md#influxdb) for the details.

## Can I query Apple Health data with DuckDB?

Yes. The server keeps every record in Parquet files, one folder per type and month, and
publishes SQL views for them. Start DuckDB in the archive folder with the views and query:

```sql
SELECT date_trunc('day', start) AS day, avg(value) AS resting_hr
FROM samples
WHERE type = 'HKQuantityTypeIdentifierRestingHeartRate'
GROUP BY day ORDER BY day DESC LIMIT 7;
```

[Data model & querying](data.md#querying-with-duckdb) explains the views. The
[query API](query-api.md) runs the same SQL over HTTP for notebooks and dashboards.

## Is there a free alternative to Health Auto Export?

Health Relay is free and open source (MIT), with every feature included. Both apps send
Apple Health data off the iPhone automatically. Health Relay sends it to its own server
program, which you run: the server stores everything losslessly in Parquet (routes, ECG
voltages, heartbeat series, deletions), and forwards numbers to VictoriaMetrics or InfluxDB.
Health Auto Export offers its automations as Premium features, and exports CSV, JSON or GPX
to destinations such as REST APIs, MQTT, Home Assistant and cloud drives.

## How is this different from the Health app's "Export All Health Data"?

The built-in export writes one zip with a large XML file, by hand, each time you want new
data. Health Relay syncs on its own in the background, a little at a time, deletions
included, and stores the data by type, ready to query.

## Which data does it sync?

Everything HealthKit offers on the iPhone, over 200 types: heart rate, HRV, resting heart
rate, sleep stages, steps, VO2 max, blood oxygen, workouts with their GPS routes, ECGs, blood
pressure, cycle tracking, medications and more. Apple Watch data reaches it through the
iPhone's Health app. [Data model & querying](data.md#what-is-synced) has the full list.

## Where does my data go?

From your iPhone to the server you pair the app with, and from there wherever you send it,
such as Grafana or a backup. The [privacy policy](privacy.md) and
[Security & privacy](security.md) describe each step.

## Does the server have to be on the internet?

The iPhone syncs whenever it can reach the server. A server on your home network gets data
while you are home; Tailscale or another VPN extends that to anywhere. See
[Networking & HTTPS](networking.md).

## Does it work on Android or with Health Connect?

Health Relay reads Apple Health on the iPhone. The server's upload API is documented in the
[server README](https://github.com/leblancfg/healthkit-relay/blob/main/server/README.md#api),
so another app could send data to it.
