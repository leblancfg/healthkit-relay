# healthkit-relay

Copy your Apple Health data, continuously, to a server you run.

Apple Health keeps years of heart rate, sleep, activity and workout data on your iPhone,
with no way to query it from elsewhere short of a manual XML export. healthkit-relay is two
small pieces:

- **`ios/`**: an iPhone app that reads HealthKit (read-only) and uploads new samples, and
  deletions, to your server. It syncs incrementally and in the background, about hourly.
- **`server/`**: a single static Go binary that receives those uploads, archives every
  record on disk, and can forward the numbers to a time-series database
  (VictoriaMetrics, InfluxDB, anything that speaks InfluxDB line protocol) for dashboards.

## Architecture

```
 iPhone                                         your server
┌──────────────────────────┐   HTTPS + token   ┌───────────────────────────────┐
│ HealthKit                │  POST /v1/samples │ healthkit-relay               │
│   │ observer queries     │ ────────────────▶ │  ├─ archive/YYYY/DATE.jsonl   │  source of truth,
│   ▼ anchored queries     │   JSON batches    │  │   (append-only, deduped)   │  append-only
│ Health Relay app         │ ◀──────────────── │  └─ line protocol ───────────▶│ VictoriaMetrics /
│  anchors · retry/backoff │   200 stored/dup  │                               │ InfluxDB (optional)
└──────────────────────────┘                   └───────────────────────────────┘
```

1. At launch the app registers an `HKObserverQuery` per data type and enables hourly
   background delivery, so iOS wakes it when new data lands.
2. For each type an `HKAnchoredObjectQuery` returns what changed since the last persisted
   anchor: new samples and deleted ones. The first run uploads all history.
3. Batches of up to 2000 records are posted with retries and exponential backoff. The
   anchor advances only after the server confirms the batch is stored, so failures lose
   nothing; the server ignores records it already has, so retries duplicate nothing.
4. While the phone is locked HealthKit data is encrypted; the app defers and syncs after
   the next unlock.
5. The server validates a batch as a whole, appends new records (one JSON line each, with
   the sample exactly as sent) to a per-day file, fsyncs, and only then answers 200.
   Deletions are recorded as tombstones; nothing is ever rewritten.

Data types are listed in one place, [`ios/HealthKitRelay/HealthTypes.swift`](ios/HealthKitRelay/HealthTypes.swift):
heart rate, HRV, resting heart rate, respiratory rate, SpO₂, VO₂ max, steps, energy,
exercise minutes, sleep stages, workouts and more. Adding a type is one line.

## Privacy

- Your data goes to your server and nowhere else: no analytics, no third-party SDKs, no
  vendor cloud. The app has no network destination other than the URL you enter.
- The app asks for **read** access only and never writes to Health.
- The server URL and token live in the iPhone Keychain (this device only), never in the
  source or the app bundle.
- The server needs a bearer token for uploads. Run it behind HTTPS (a reverse proxy, or a
  private network such as a VPN or Tailscale); do not expose it publicly without TLS.
- `/healthz` and `/metrics` carry no health data (`/metrics` exposes request counts).
- The archive is plain JSON Lines: readable with `jq`, easy to back up, easy to delete.

## Quick start

Server (needs Go 1.24+, or `nix build`):

```sh
make server-build
head -c 32 /dev/urandom | base64 > token     # keep this file private (chmod 600)
./bin/healthkit-relay -listen 127.0.0.1:8080 -archive-dir ./archive -token-file ./token \
  -forward-url http://127.0.0.1:8428/influx/write   # optional
```

See [server/README.md](server/README.md) for the API, the archive format and all flags.

iPhone app (needs a Mac with Xcode, [XcodeGen](https://github.com/yonaskolb/XcodeGen) and
a paid Apple Developer account, which HealthKit requires):

```sh
cp ios/Config/Local.xcconfig.example ios/Config/Local.xcconfig   # set your team id and bundle id
make ios-build    # or: make ios-project, then open ios/HealthKitRelay.xcodeproj
```

Then open **Health Relay** on the phone, enter the server URL and token, tap **Grant
access** and **Sync now**. See [ios/README.md](ios/README.md).

## Layout

```
server/     Go receiver (stdlib only), tests
ios/        SwiftUI app; project.yml is the XcodeGen spec
flake.nix   Nix package for the server
Makefile    server-test, server-build, ios-build, ios-test
```

## License

MIT, see [LICENSE](LICENSE).
