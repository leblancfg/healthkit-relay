# healthkit-relay

Copy your Apple Health data, continuously, to a server you run.

Apple Health keeps years of heart rate, sleep, activity and workout data on your iPhone,
with no way to query it from elsewhere short of a manual XML export. healthkit-relay is two
small pieces:

- **`ios/`**: an iPhone app that reads HealthKit (read-only) and uploads new samples, and
  deletions, to your server. It syncs incrementally and in the background, about hourly.
- **`server/`**: a single static Go binary that receives those uploads, archives every
  record on disk as Parquet files you can query with DuckDB, and can forward the numbers to a
  time-series database (VictoriaMetrics, InfluxDB, anything that speaks InfluxDB line
  protocol) for dashboards.

## Architecture

```
 iPhone                                         your server
┌──────────────────────────┐   HTTPS + token   ┌───────────────────────────────┐
│ HealthKit                │  POST /v1/samples │ healthkit-relay               │
│   │ observer queries     │ ────────────────▶ │  ├─ staging.db (SQLite)       │  acked once committed,
│   ▼ anchored queries     │   JSON batches    │  │   └─ hourly ─▶ parquet/    │  deduped; Parquet by
│ Health Relay app         │ ◀──────────────── │  └─ line protocol ───────────▶│  type and month
│  anchors · retry/backoff │   200 stored/dup  │                               │ VictoriaMetrics /
│                          │                   │                               │ InfluxDB (optional)
└──────────────────────────┘                   └───────────────────────────────┘
```

1. At launch the app registers an `HKObserverQuery` per data type and enables hourly
   background delivery, so iOS wakes it when new data lands.
2. For each type an `HKAnchoredObjectQuery` returns what changed since the last persisted
   anchor: new samples and deleted ones. The first run uploads all history.
3. Batches (up to 4 MB) are posted with retries and exponential backoff; long series such
   as routes and ECG voltages are split into chunks. The anchor advances only after the
   server confirms the whole page is stored, so failures lose nothing and an interrupted
   first upload resumes where it stopped; the server ignores records it already has, so
   retries duplicate nothing.
4. While the phone is locked HealthKit data is encrypted; the app defers and syncs after
   the next unlock.
5. The server validates a batch as a whole, commits the new records (each with the sample
   exactly as sent) to a SQLite staging database, and only then answers 200. Every hour they
   move to Parquet files partitioned by data type and month; every night each month's files
   are merged into one. Deletions are recorded as tombstones and applied at the next merge.

### What it syncs: everything

There is no hand-written list of data types. At build time,
[`ios/Scripts/gen-health-types`](ios/Scripts/gen-health-types) reads the HealthKit headers of
the SDK the app is compiled with and generates every type identifier HealthKit declares, with
Apple's documented unit for each quantity and the names of every enum value
([`Generated/SDKHealthTypes.swift`](ios/HealthKitRelay/Generated/SDKHealthTypes.swift)). Build
with a newer Xcode and the types Apple added are synced, with no code change; types the
phone's iOS does not have yet are skipped. With the iOS 27 SDK that is 222 types:

- all 121 quantity types (heart rate, HRV, steps, energy, nutrition, mobility, audio
  exposure...) and 71 category types (sleep stages, symptoms, cycle tracking, events...), with
  full metadata, source, device and the individual values of series samples;
- workouts with their activities, events, per-type statistics and routes (every GPS point);
- heartbeat series (beat-to-beat timing), ECGs (every voltage sample, classification,
  symptoms), audiograms, blood pressure and food correlations with their members, state of
  mind, GAD-7 and PHQ-9 assessments, medications and dose events, vision prescriptions (with
  attached scans), health records (FHIR) and CDA documents;
- daily activity summaries (rings and goals) and characteristics (date of birth, biological
  sex, blood type, skin type, wheelchair use, move mode).

Nothing is dropped for want of a unit: each quantity type gets one unit for its whole history
(the one the Health app shows, else Apple's canonical one), and every value is sent with its
unit string. Each object is also uploaded with its complete `NSKeyedArchiver` archive (except
the bulk quantity and category samples), so even properties this app has no field for yet are
kept.

## Privacy

- Your data goes to your server and nowhere else: no analytics, no third-party SDKs, no
  vendor cloud. The app has no network destination other than the URL you enter.
- The app asks for **read** access only and never writes to Health.
- The server URL and the app's token live in the iPhone Keychain (this device only), never
  in the source or the app bundle. Each iPhone gets a token of its own when it scans a setup
  code; the server keeps only a hash of it and can revoke it.
- The server needs a token for uploads. Run it behind HTTPS (a reverse proxy, or a private
  network such as a VPN or Tailscale); do not expose it publicly without TLS. Setup and
  pairing answer private networks only, by default.
- `/healthz` and `/metrics` carry no health data (`/metrics` exposes request counts).
- The archive is plain Parquet files (DuckDB, pandas, Polars, Spark read them), with each
  record also kept as the JSON the app sent. The app's **Erase Server Data and Start Over**
  deletes everything that iPhone uploaded, for good, then uploads it again.

## Quick start

Server (needs Go 1.26+, or `nix build`):

```sh
make server-build
head -c 32 /dev/urandom | base64 > token     # keep this file private (chmod 600)
./bin/healthkit-relay -listen 127.0.0.1:8080 -archive-dir ./archive -token-file ./token \
  -forward-url http://127.0.0.1:8428/influx/write   # optional
```

See [server/README.md](server/README.md) for the API, the archive format, DuckDB queries
([`server/sql/views.sql`](server/sql/views.sql)) and all flags, and
[docs/install.md](docs/install.md) for Docker, release binaries with systemd, and NixOS.
Upgrading from 0.4 or earlier: stop the server and run
`healthkit-relay migrate -archive-dir ./archive` once.

iPhone app (needs a Mac with Xcode, [XcodeGen](https://github.com/yonaskolb/XcodeGen) and
a paid Apple Developer account, which HealthKit requires):

```sh
cp ios/Config/Local.xcconfig.example ios/Config/Local.xcconfig   # set your team id and bundle id
make ios-build    # or: make ios-project, then open ios/HealthKitRelay.xcodeproj
```

Then set up the app in one tap: open `http://127.0.0.1:8080/setup` (or your server's
address) in a browser, enter the token, and scan the code with the iPhone's Camera. Or, on
the server, run `./bin/healthkit-relay pair -token-file ./token -public-url <address phones
use>` and scan the code it prints. The app connects, asks for Health access once (select
everything, then confirm) and starts syncing; there is nothing else to set. See
[server/README.md](server/README.md#setting-up-the-app) for how pairing works, and
[ios/README.md](ios/README.md).

## Layout

```
server/     Go receiver and archive (SQLite staging, Parquet), tests; sql/views.sql for DuckDB
ios/        SwiftUI app; project.yml is the XcodeGen spec
flake.nix   Nix package and NixOS module (nix/) for the server
docs/       install guide: Docker, binary + systemd, NixOS
packaging/  systemd unit shipped in the release archives
scripts/    smoke-test.sh, the end-to-end check every install path runs in CI
Makefile    server-test, server-build, ios-build, ios-test
```

## License

MIT, see [LICENSE](LICENSE).
