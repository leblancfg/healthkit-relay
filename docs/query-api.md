# Query API

A read-only HTTP API over the Parquet archive: which HealthKit types have data, their samples,
and daily or hourly aggregates (steps per day, average heart rate per hour, hours of deep
sleep per night). It is described by an OpenAPI 3.1 document at `/openapi.json`, browsable at
`/docs`, so dashboards, scripts and LLM agents can discover it on their own.

It is **off by default**. There is no SQL endpoint: every query is built by the server from
typed, validated parameters.

## Turning it on

The server runs each query with the [DuckDB](https://duckdb.org) command-line tool (the server
itself stays a static Go binary). Install `duckdb` (1.2 or newer, for `allowed_directories`; tested with 1.5) and point the server at it:

```sh
healthkit-relay -archive-dir /var/lib/healthkit-relay -token-file /etc/healthkit-relay/token \
  -query-duckdb /usr/bin/duckdb
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

The query API takes a **read token**, a token of its own for each client:

```sh
healthkit-relay token create -name grafana > grafana.token   # prints the token once
healthkit-relay devices                                       # lists it, with SCOPE read
healthkit-relay revoke <id>                                    # revokes it
```

or **Read tokens** on the setup page (signed in with the server token), or
`POST /v1/tokens` with the server token. The server keeps only the token's SHA-256, in
`devices.json`, next to the paired iPhones.

| Token | Scope | Query API | Upload, status, erase | Tokens, devices, pairing |
|---|---|---|---|---|
| Server token | admin | yes | yes | yes |
| Read token | `read` | yes | no (`403`) | no (`401`) |
| Paired iPhone | `upload` | no (`403`) | yes, its own data | no (`401`) |

Devices paired before scopes existed have no `scope` in `devices.json`: they are upload
tokens, and the file keeps working unchanged. `/openapi.json` and `/docs` need no token: they
hold no health data.

## Endpoints

All `GET`, with `Authorization: Bearer <token>`.

| Path | |
|---|---|
| `/v1/kinds` | kinds of samples (`quantity`, `category`, `workout`...) |
| `/v1/types` | every HealthKit type, with kind, unit, aggregation style, category labels and what the archive holds (count, first and last sample); `?present=true`, `?kind=` |
| `/v1/types/{type}` | one type |
| `/v1/types/{type}/samples` | its samples in `[start, end)`, paginated (`limit`, `cursor`, `order`, `raw`) |
| `/v1/types/{type}/aggregates` | per day or hour (`interval`), in a time zone (`tz`) |

`{type}` is a HealthKit identifier such as `HKQuantityTypeIdentifierStepCount`; the list is
generated from the iOS SDK the app is built with, and the document publishes it as an enum.
Times are RFC 3339; `start` and `end` also take a date, meaning midnight in `tz`.

```sh
T=$(cat grafana.token)
# Steps per day, last week, days in Toronto time
curl -H "Authorization: Bearer $T" \
  'http://127.0.0.1:8080/v1/types/HKQuantityTypeIdentifierStepCount/aggregates?tz=America/Toronto&start=2026-10-11&end=2026-10-18'
# Sleep stages per day
curl -H "Authorization: Bearer $T" \
  'http://127.0.0.1:8080/v1/types/HKCategoryTypeIdentifierSleepAnalysis/aggregates?tz=America/Toronto'
# The last 10 heart rate readings
curl -H "Authorization: Bearer $T" \
  'http://127.0.0.1:8080/v1/types/HKQuantityTypeIdentifierHeartRate/samples?order=desc&limit=10'
```

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
