# GitHub repository metadata

To apply once on GitHub (repository › About, gear icon), or with the `gh` commands below.
This file is excluded from the docs site.

## About (description, max 350 characters)

> Self-hosted Apple Health export: sync iPhone and Apple Watch HealthKit data (heart rate, HRV, sleep, workouts, GPS routes, ECG) to your own server, and only there. Lossless Parquet archive, DuckDB and a read-only query API, Grafana via VictoriaMetrics/InfluxDB.

## Website

https://health-relay.leblancfg.com/

## Topics (max 20)

```
apple-health
healthkit
apple-watch
self-hosted
health-data
quantified-self
ios
swiftui
golang
grafana
prometheus
victoriametrics
influxdb
parquet
duckdb
privacy
homelab
heart-rate-variability
sleep-tracking
data-export
```

## Apply with gh

```sh
gh repo edit leblancfg/healthkit-relay \
  --description "Self-hosted Apple Health export: sync iPhone and Apple Watch HealthKit data (heart rate, HRV, sleep, workouts, GPS routes, ECG) to your own server, and only there. Lossless Parquet archive, DuckDB and a read-only query API, Grafana via VictoriaMetrics/InfluxDB." \
  --homepage "https://health-relay.leblancfg.com/" \
  --add-topic apple-health,healthkit,apple-watch,self-hosted,health-data,quantified-self,ios,swiftui,golang,grafana,prometheus,victoriametrics,influxdb,parquet,duckdb,privacy,homelab,heart-rate-variability,sleep-tracking,data-export
```

## Other settings when the repository goes public

- **Settings › Pages › Source: Deploy from a branch, `gh-pages`**, custom domain
  `health-relay.leblancfg.com` (the `docs` workflow, or `mkdocs gh-deploy`, publishes it).
- **Settings › Code security › Private vulnerability reporting: enable** (SECURITY.md relies
  on it).
- **Social preview image** (Settings › General › Social preview › Edit › Upload an
  image): `docs/assets/social-github.png`, 1280×640, drawn by `scripts/social-image.py`.
- Enable **Discussions** if you want questions out of the issue tracker.
