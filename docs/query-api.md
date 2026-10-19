# Query API

A read-only HTTP API over the Parquet archive: which HealthKit types have data, their samples,
and daily or hourly aggregates (steps per day, average heart rate per hour, minutes of deep
sleep per night). It is described by an OpenAPI 3.1 document at `/openapi.json`, browsable at
`/docs`, so dashboards, scripts and LLM agents can discover it on their own.

It is **off by default**; `-query-duckdb` turns it on.

## Types and kinds

A **type** is one specific HealthKit measurement, named by HealthKit's identifier:
`HKQuantityTypeIdentifierStepCount`, `HKQuantityTypeIdentifierHeartRate`,
`HKCategoryTypeIdentifierSleepAnalysis`. A **kind** is the shape of a type's data. Each type
has one kind: `HKQuantityTypeIdentifierStepCount` is a type of kind `quantity`.

| Kind | Shape | Example |
|---|---|---|
| `quantity` | a number with a unit | steps: 12027 `count` on 2026-10-15; heart rate: 67 `count/min` at 09:04 |
| `category` | a labelled interval | sleep: `asleepDeep` from 01:10 to 01:40; stand hours: `stood` from 14:00 to 15:00 |
| `workout`, `heartbeat_series`, `electrocardiogram`, `audiogram`, `correlation`, ... | a record with fields of its own | a run with its duration, energy and distance; an ECG with its classification |

Quantity and category types have aggregates; for the other kinds, read the samples with
`raw=true`. `GET /v1/types` lists every type with its kind; `GET /v1/types?kind=category` lists
the types of one kind.

## Turning it on

The server runs each query with the [DuckDB](https://duckdb.org) command-line tool (the server
itself stays a static Go binary). Install `duckdb` (1.2 or newer, for `allowed_directories`; tested with 1.5) and point the server at it:

```sh
healthkit-relay -archive-dir /var/lib/healthkit-relay -query-duckdb /usr/bin/duckdb
```

| Flag | Default | |
|------|---------|-|
| `-query-duckdb` | (off) | the `duckdb` executable; empty turns the query API off |
| `-query-archive-dir` | `-archive-dir` | the archive whose `parquet/` is read, e.g. a replica or a restored backup |
| `-query-timeout` | `30s` | longest a query may run, waiting for a free slot included; the process is killed at the deadline |
| `-query-max-rows` | `50000` | most rows one query may return |
| `-query-memory-limit` | `512MB` | DuckDB `memory_limit` per query; nothing spills to disk, so a query needing more fails |
| `-query-threads` | `2` | DuckDB threads per query |
| `-query-concurrency` | `2` | queries that may run at once; others wait, then get `503` |

Like every flag they can be set from the environment (`HKR_QUERY_DUCKDB`, ...). Then open
`http://<server>/docs`.

Samples received in the last hour may still be in the staging database (flushed to Parquet
hourly) and are not visible to the API until then.

## Tokens

The query API takes a **read token**, a token of its own for each client. The quickest way
to get one: open **`/setup#read-tokens`** (the `/docs` page links to it), sign in with the
admin token, type a name under **Read tokens** and click **Create**. The token is shown once,
with a **Copy** button: save it in a password manager, then paste it into the client
(Grafana, a script, an agent) as a Bearer token, `Authorization: Bearer <token>`. Lost it?
Revoke it and create a new one. Over plain HTTP on a LAN address, where the browser offers no
clipboard access, **Copy** selects the token for Ctrl+C or Cmd+C.

On the server, as the server's user or root, with its archive directory (see
[Commands](setup/index.md#commands)):

```sh
healthkit-relay token create -archive-dir /var/lib/healthkit-relay -name grafana > grafana.token   # prints the token once
healthkit-relay devices -archive-dir /var/lib/healthkit-relay                                       # lists it, with SCOPE read
healthkit-relay revoke -archive-dir /var/lib/healthkit-relay <id>                                    # revokes it
```

(`docker compose exec relay healthkit-relay token create -name grafana` in Docker,
`sudo healthkit-relay-ctl token create -name grafana` on NixOS), or `POST /v1/tokens` with
the admin token. The server keeps only the token's SHA-256, in
`devices.json`, next to the paired iPhones.

| Token | Scope | Query API | Upload, status, erase | Tokens, devices, pairing |
|---|---|---|---|---|
| Admin token | admin | yes | erase any device's data; uploads deprecated | yes |
| Read token | `read` | yes | no (`403`) | no (`401`) |
| Paired iPhone | `upload` | no (`403`) | yes, its own data | no (`401`) |

Devices paired before scopes existed have no `scope` in `devices.json`: they are upload
tokens, and the file keeps working unchanged. `/openapi.json` and `/docs` are public.

## Endpoints

All `GET`, with `Authorization: Bearer <token>`.

| Path | |
|---|---|
| `/v1/types` | every HealthKit type, with kind, unit, aggregation style, category labels and what the archive holds (count, first and last sample); `?present=true`, `?kind=` |
| `/v1/types/{type}` | one type |
| `/v1/types/{type}/samples` | its samples in `[start, end)`, paginated (`limit`, `cursor`, `order`, `raw`) |
| `/v1/types/{type}/aggregates` | per day or hour (`interval`), in a time zone (`tz`) |

`{type}` is a HealthKit identifier such as `HKQuantityTypeIdentifierStepCount`; the list is
generated from the iOS SDK the app is built with, and the document publishes it as an enum.
Times are RFC 3339; `start` and `end` also take a date, meaning midnight in `tz`.

## Examples

```sh
T=$(cat grafana.token)
H="Authorization: Bearer $T"
```

Which types have data, of one kind:

```sh
curl -H "$H" 'http://127.0.0.1:8080/v1/types?present=true&kind=category'
```

```json
{"types": [
  {"identifier": "HKCategoryTypeIdentifierAppleStandHour", "kind": "category",
   "display_name": "Apple Stand Hour", "value_enum": "HKCategoryValueAppleStandHour",
   "values": [{"value": 0, "label": "stood"}, {"value": 1, "label": "idle"}], "aggregates": true,
   "data": {"samples": 41632, "first_start": "2021-06-30T00:00:00Z", "last_start": "2026-10-18T19:00:00Z"}},
  ...
]}
```

Steps per day, days in Toronto time:

```sh
curl -H "$H" 'http://127.0.0.1:8080/v1/types/HKQuantityTypeIdentifierStepCount/aggregates?tz=America/Toronto&start=2026-10-15&end=2026-10-17'
```

```json
{"type": "HKQuantityTypeIdentifierStepCount", "kind": "quantity", "aggregation": "cumulative",
 "interval": "day", "tz": "America/Toronto", "dedup": "priority",
 "start": "2026-10-15T00:00:00-04:00", "end": "2026-10-17T00:00:00-04:00",
 "buckets": [
  {"start": "2026-10-15T00:00:00-04:00", "end": "2026-10-16T00:00:00-04:00", "date": "2026-10-15",
   "unit": "count", "sum": 12027, "samples": 1009, "sources": ["Apple Watch", "iPhone"]},
  {"start": "2026-10-16T00:00:00-04:00", "end": "2026-10-17T00:00:00-04:00", "date": "2026-10-16",
   "unit": "count", "sum": 4895, "samples": 88, "sources": ["Apple Watch", "iPhone"]}
 ]}
```

Average heart rate per hour:

```sh
curl -H "$H" 'http://127.0.0.1:8080/v1/types/HKQuantityTypeIdentifierHeartRate/aggregates?interval=hour&tz=America/Toronto&start=2026-10-17T09:00:00-04:00&end=2026-10-17T10:00:00-04:00'
```

```json
{"type": "HKQuantityTypeIdentifierHeartRate", "kind": "quantity", "aggregation": "discreteTemporallyWeighted",
 "interval": "hour", "tz": "America/Toronto", ...,
 "buckets": [
  {"start": "2026-10-17T09:00:00-04:00", "end": "2026-10-17T10:00:00-04:00", "unit": "count/min",
   "avg": 123.98, "min": 67, "max": 170, "samples": 452, "sources": ["Apple Watch"]}
 ]}
```

Sleep stages per day (seconds per label):

```sh
curl -H "$H" 'http://127.0.0.1:8080/v1/types/HKCategoryTypeIdentifierSleepAnalysis/aggregates?tz=America/Toronto&start=2026-10-18&end=2026-10-19'
```

```json
{"type": "HKCategoryTypeIdentifierSleepAnalysis", "kind": "category", "interval": "day", ...,
 "buckets": [
  {"date": "2026-10-18", ..., "values": [
    {"value": 3, "label": "asleepCore", "samples": 14, "seconds": 13500, "sources": ["Apple Watch"]},
    {"value": 4, "label": "asleepDeep", "samples": 5, "seconds": 3300, "sources": ["Apple Watch"]},
    {"value": 5, "label": "asleepREM", "samples": 6, "seconds": 4920, "sources": ["Apple Watch"]}
   ], "samples": 25, "sources": ["Apple Watch"]}
 ]}
```

Individual heart rate readings, two per page:

```sh
curl -H "$H" 'http://127.0.0.1:8080/v1/types/HKQuantityTypeIdentifierHeartRate/samples?tz=America/Toronto&start=2026-10-17T09:00:00-04:00&limit=2'
```

```json
{"type": "HKQuantityTypeIdentifierHeartRate", "start": "2026-10-17T09:00:00-04:00", ..., "count": 2,
 "samples": [
  {"uuid": "85DB0380-1610-44BD-A87C-C39D3BF81A65", "start": "2026-10-17T09:04:13.345-04:00",
   "end": "2026-10-17T09:04:13.345-04:00", "value": 67, "unit": "count/min",
   "source": {"name": "Apple Watch", "device": "Apple Watch", "product_type": "Watch6,14", ...}},
  ...
 ],
 "next_cursor": "MTphOjE3OTIyNDI0ODEzNDUwMDA6OTlGQUE4MjItOTdFOC00RjNELUExMUItM0M3NjY1MjBDNUNG"}
```

Pass `next_cursor` as `&cursor=...`, with the same other parameters, for the next page.

## Aggregates

What a bucket holds depends on the type:

- **Cumulative quantities** (steps, distances, energy, exercise minutes): `sum`. A sample
  spanning two buckets is split in proportion to the time in each.
- **Discrete quantities** (heart rate, HRV, body mass...): `avg`, `min`, `max` of the samples
  starting in the bucket. Sound exposure averages as energy (`10·log10(mean(10^(dB/10)))`),
  as Apple does; temporally weighted types are averaged per sample, not weighted by duration.
- **Categories** (sleep, stand hours, symptoms...): per value, the samples and the seconds
  they cover inside the bucket. A night's sleep is cut at local midnight.

At most 366 days or 744 hours per request. Days are local calendar days of `tz` (23 or 25
hours across a DST change).

### Counting each moment once (`dedup`)

An Apple Watch and an iPhone carried together both count steps, distance and energy; other
apps may write the same thing again. Summing every sample overcounts. With `dedup=priority`,
the default, sources are ranked **Apple Watch, then iPhone, then any other app or device**
(from the sample's hardware model, e.g. `Watch6,14`), and a sample counts only for the part of
its time that no sample of a higher-ranked source covers; for categories, only a sample of the
same value covers (the watch's sleep stages do not hide the phone's *in bed*). This is how
Apple Health merges sources with its default source order: the watch wins where both
measured, and the phone fills the time the watch was off the wrist.

Checked against the daily totals Apple's Activity app computes (the activity summaries in the
archive), over 365 days of one person's data with two iPhones and a watch:

| Rule | Active energy, mean daily error | Exercise minutes, mean daily error |
|---|---|---|
| `priority` (default) | 6.5 kcal (0.8%) | 1.3 min (2.3%) |
| one source per hour (watch first) | 4.7 kcal | 4.7 min (drops phone-only minutes) |
| largest source per hour | 17.6 kcal | 1.2 min |
| `none` (sum everything) | 36.9 kcal (4.7%) | 10.4 min (19%) |

`dedup=none` sums every sample, for when you want each source's count.

## How queries run

Each query is a fresh `duckdb` process, in memory, reading the archive through the views in
[`server/sql/views.sql`](https://github.com/leblancfg/healthkit-relay/blob/main/server/sql/views.sql)
(so deleted samples stay hidden). Before the query it sets:

- `enable_external_access = false` with `allowed_directories` = the archive's `parquet/`:
  no other file, no network, no `getenv`;
- `autoinstall_known_extensions = false`, `autoload_known_extensions = false`;
- `memory_limit`, `threads`, `temp_directory = ''` (no spilling to disk),
  `preserve_insertion_order = false`;
- `lock_configuration = true`, so nothing after it can change those.

It runs with an empty environment, as the server's user, and every query filters on the type
and the months it needs (the archive's partitions), so it reads only those files.
