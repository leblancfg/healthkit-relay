# Query API

The **query API** lets a program read your health data from the server: a Grafana dashboard,
a script, or an AI agent. It answers questions such as "steps per day last week", "average
heart rate per hour" or "minutes of deep sleep per night". It reads the
[archive](glossary.md#archive), the folder where the server keeps your data, and it only reads: no
request changes anything.

An API (application programming interface) is a set of web addresses that answer programs
instead of people. Each answer is JSON, a text format that programs read easily. The query
API is described by an OpenAPI 3.1 document at `/openapi.json` and shown as a page you can
try out at `/docs`, e.g. `http://192.168.1.10:8080/docs`.

To use it you need:

- the **server address**, the address the iPhone uses to reach the server, e.g.
  `http://192.168.1.10:8080`;
- a **read token**: a password for one program, that reads your data through the query API
  and does nothing else. You create one for each program, e.g. one named `grafana`.

The query API starts when the server has the `-query-duckdb` setting; until then, its
addresses answer `404`.

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

Quantity and category types have aggregates (totals and averages per day or hour). For the
other kinds, read the samples with `raw=true`. `GET /v1/types` lists every type with its
kind; `GET /v1/types?kind=category` lists the types of one kind.

## Turn it on

The server runs each query with [DuckDB](https://duckdb.org), a database program that reads
the archive's Parquet files. You install the `duckdb` program next to the server, then tell
the server where it is.

1. Install `duckdb`, version 1.2 or newer (tested with 1.5), on the computer that runs the
   server:

    ```sh
    brew install duckdb          # macOS
    ```

    On Linux, download the `duckdb` command-line program for your system from
    [duckdb.org](https://duckdb.org/docs/installation/) and copy it to `/usr/local/bin`.
    Check it:

    ```sh
    duckdb --version
    ```

    It prints a version such as `v1.5.0`.

2. Give the server the path of `duckdb` with `-query-duckdb` (or `HKR_QUERY_DUCKDB`):

    - Linux with systemd: add `HKR_QUERY_DUCKDB=/usr/local/bin/duckdb` to
      `/etc/healthkit-relay/env`, then `sudo systemctl restart healthkit-relay`.
    - macOS: add `<string>-query-duckdb</string><string>/opt/homebrew/bin/duckdb</string>`
      (the path `which duckdb` prints) to the `ProgramArguments` of the launchd agent (see
      [macOS](setup/macos.md)), run
      `launchctl bootout gui/$(id -u)/io.github.healthkit-relay`, then the
      `launchctl bootstrap` command from that page again.
    - NixOS: `services.healthkit-relay.extraFlags = [ "-query-duckdb" "${pkgs.duckdb}/bin/duckdb" ];`,
      then `sudo nixos-rebuild switch`.
    - From a terminal:

        ```sh
        healthkit-relay -archive-dir /var/lib/healthkit-relay -query-duckdb /usr/local/bin/duckdb
        ```

    The Docker image holds the server program alone. To use the query API, run the server
    from one of the setups above.

3. Read the server's log. It shows a line like this one:

    ```text
    query API on: /usr/local/bin/duckdb reads /var/lib/healthkit-relay/parquet; /docs, /openapi.json
    ```

    A second line with `the test query failed` means `duckdb` could not read the archive:
    the line ends with DuckDB's reason.

4. Open the `/docs` page in a browser, e.g. `http://192.168.1.10:8080/docs`. It shows
   **Health Relay query API**, with every endpoint.

Samples reach the Parquet files at the hourly flush. Samples the server received in the last
hour appear in the query API after the next flush.

### Settings

| Flag | Default | |
|------|---------|-|
| `-query-duckdb` | (empty: the query API is off) | the `duckdb` program |
| `-query-archive-dir` | `-archive-dir` | the archive whose `parquet/` is read, e.g. a replica or a restored backup |
| `-query-timeout` | `30s` | longest a query may run, waiting for a free slot included; DuckDB is stopped at the deadline |
| `-query-max-rows` | `50000` | most rows one query may return |
| `-query-memory-limit` | `512MB` | DuckDB `memory_limit` per query; a query needing more fails (it never spills to disk) |
| `-query-threads` | `2` | DuckDB threads per query |
| `-query-concurrency` | `2` | queries that may run at once; the others wait, up to `-query-timeout`, then get `503` |

Each flag has an environment variable: `HKR_` and the name in capitals, with `_` for `-`
(`HKR_QUERY_TIMEOUT=60s`).

## Get a read token

You create read tokens on the server's **setup page**, its `/setup` page. You sign in to it
with the **admin token**, the password of your server, which the server printed on its first
start (see [Troubleshooting](troubleshooting.md#where-is-the-admin-token) if you do not have
it). The `/docs` page links to the right place.

1. On a computer on your home network (or your Tailscale network), open the setup page at
   its read tokens section, e.g. `http://192.168.1.10:8080/setup#read-tokens`. The page asks
   for the **Admin token**.
2. Paste the admin token and click **Sign in**. The page shows **Read tokens**, below
   **Pair your iPhone** and **Paired devices**.
3. Under **Read tokens**, type a name that says what the token is for, e.g. `grafana`, in
   the box that says *Name, e.g. grafana*. Click **Create**.
4. The page shows **Read token for grafana** and the token, a line of 43 letters, digits,
   `-` and `_`. Click the copy button next to it. On a plain `http://` address the button
   selects the token instead: press Ctrl+C (Cmd+C on a Mac).
5. Paste the token into your password manager now. The server keeps only a fingerprint of
   it (its SHA-256) and shows it this once.
6. Click **Done**. The **Read tokens** list now shows `grafana`, with the date it was
   created.

To stop a read token, click **Revoke** next to it under **Read tokens**. It stops working at
once. Lost a token? Revoke it and create a new one.

### From a terminal

On the server, run `token create` as the server's user or as root, with the server's
archive directory (see [Commands](setup/index.md#commands)):

```sh
healthkit-relay token create -archive-dir /var/lib/healthkit-relay -name grafana
```

```text
q3vN8x2LkP0aTzR7mW5cYb1HdJ9eFgUs4oXiK6nVtQA
Created read token 4be1f07a (grafana). It is shown only this once; revoke it with: healthkit-relay revoke 4be1f07a
```

The first line is the token. With Docker Compose, run
`docker compose exec relay healthkit-relay token create -name grafana`; on NixOS,
`sudo healthkit-relay-ctl token create -name grafana`.

`healthkit-relay devices` lists paired devices and read tokens, with `read` in the `SCOPE`
column for read tokens; `healthkit-relay revoke 4be1f07a` revokes one.

A program can also create a token with `POST /v1/tokens` and the admin token:

```sh
curl -X POST -H "Authorization: Bearer $ADMIN_TOKEN" -d '{"name": "grafana"}' \
  http://192.168.1.10:8080/v1/tokens
```

```json
{"token":"q3vN8x2LkP0aTzR7mW5cYb1HdJ9eFgUs4oXiK6nVtQA","device":{"id":"4be1f07a","name":"grafana","created_at":"2026-10-19T14:02:11Z","last_seen_at":"2026-10-19T14:02:11Z","scope":"read"}}
```

### What each token may do

The server keeps the SHA-256 of every token in `devices.json`, in the archive directory:
read tokens next to the [paired devices](glossary.md#paired-device), the iPhones that used a setup code.
Each token has a scope, the list of what it may do.

| Token | Scope | Query API | Upload, status, erase | Tokens, devices, pairing |
|---|---|---|---|---|
| Admin token | admin | yes | erase any paired device's data; uploads are deprecated | yes |
| Read token | `read` | yes | no (`403`) | no (`401`) |
| A paired device's token | `upload` | no (`403`) | yes, its own data | no (`401`) |

A paired device from a version before scopes has no `scope` in `devices.json` and counts as
`upload`. `/openapi.json` and `/docs` are public: they describe the API and hold no data.

## Your first query

This sends one request with `curl`, a command-line program for web requests (installed on
macOS and most Linux systems). Run it on any computer that reaches the server.

1. Put the server address and your read token in two variables, so the next commands can
   use them:

    ```sh
    SERVER=http://192.168.1.10:8080
    H="Authorization: Bearer q3vN8x2LkP0aTzR7mW5cYb1HdJ9eFgUs4oXiK6nVtQA"
    ```

2. Ask which types have data:

    ```sh
    curl -H "$H" "$SERVER/v1/types?present=true"
    ```

    The answer starts with `{"types":[{"identifier":"HKQuantityTypeIdentifierStepCount",...`.
    It is one long line; the examples below spread it over several lines to read it.

3. If it answers something else:

    | Answer | Meaning | What to do |
    |---|---|---|
    | `{"error":"unauthorized"}` | the server does not know this token | check that you pasted the whole token; create a new one if it was revoked |
    | another `{"error": ...}` | the server refused the request | read the message: it says what to change, e.g. that a paired device's token cannot query |
    | `404 page not found` | the query API is off | [turn it on](#turn-it-on) |
    | `Failed to connect` | curl cannot reach the server | check the address, as in [Troubleshooting](troubleshooting.md#other-computers-cannot-connect) |

4. Ask for steps per day, with days in your time zone:

    ```sh
    curl -H "$H" "$SERVER/v1/types/HKQuantityTypeIdentifierStepCount/aggregates?tz=America/Toronto&start=2026-10-15&end=2026-10-17"
    ```

    The answer has one bucket per day, with `"sum": 12027` and the like. More examples
    follow.

## Endpoints

All `GET`, with `Authorization: Bearer <token>`.

| Path | |
|---|---|
| `/v1/types` | every HealthKit type, with kind, unit, aggregation style, category labels and what the archive holds (count, first and last sample); `?present=true` keeps the types with data, `?kind=category` the types of one kind |
| `/v1/types/{type}` | one type |
| `/v1/types/{type}/samples` | its samples in `[start, end)`, page by page (`limit`, `cursor`, `order`, `raw`) |
| `/v1/types/{type}/aggregates` | per day or hour (`interval`), in a time zone (`tz`) |

`{type}` is a HealthKit identifier such as `HKQuantityTypeIdentifierStepCount`. In the
OpenAPI document it is a string that starts with `HK` (the pattern
`^HK[A-Za-z]+Type[A-Za-z0-9]*$`), with examples; `GET /v1/types` lists the valid values,
over 200 of them, from the iOS SDK the app is built with. An unknown type answers `400` with
the closest names:

```sh
curl -H "$H" "$SERVER/v1/types/HKQuantityTypeIdentifierSteps"
```

```json
{"error":"unknown type HKQuantityTypeIdentifierSteps; did you mean HKQuantityTypeIdentifierStepCount?"}
```

`kind` takes one of the kinds the OpenAPI document lists (`quantity`, `category`,
`workout`, ...).

Times are RFC 3339 timestamps with an offset, e.g. `2026-10-17T09:00:00-04:00`. `start` and
`end` also take a date, e.g. `2026-10-17`, meaning midnight in `tz`. `tz` is a time zone name
such as `America/Toronto`; the default is UTC.

### Errors

Every error is JSON with an `error` message, e.g.
`{"error":"tz \"Mars/Olympus\" is not in the time zone database"}`.

| Status | When |
|---|---|
| `400` | a parameter is wrong: an unknown type, a bad date, a range over the limit |
| `401` | no token, or a token the server does not know |
| `403` | a paired device's token |
| `422` | the answer would be too large: ask for fewer rows or a shorter range |
| `503` | too many queries at once: try again after the `Retry-After` seconds |
| `504` | the query took longer than `-query-timeout`: ask for a shorter range |

## Examples

These use the `SERVER` and `H` variables from [Your first query](#your-first-query).

Which types have data, of one kind:

```sh
curl -H "$H" "$SERVER/v1/types?present=true&kind=category"
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
curl -H "$H" "$SERVER/v1/types/HKQuantityTypeIdentifierStepCount/aggregates?tz=America/Toronto&start=2026-10-15&end=2026-10-17"
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
curl -H "$H" "$SERVER/v1/types/HKQuantityTypeIdentifierHeartRate/aggregates?interval=hour&tz=America/Toronto&start=2026-10-17T09:00:00-04:00&end=2026-10-17T10:00:00-04:00"
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
curl -H "$H" "$SERVER/v1/types/HKCategoryTypeIdentifierSleepAnalysis/aggregates?tz=America/Toronto&start=2026-10-18&end=2026-10-19"
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
curl -H "$H" "$SERVER/v1/types/HKQuantityTypeIdentifierHeartRate/samples?tz=America/Toronto&start=2026-10-17T09:00:00-04:00&limit=2"
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

For the next page, send the same request with `&cursor=` and the `next_cursor` value. The
last page has no `next_cursor`.

## Aggregates

What a bucket holds depends on the type:

- **Cumulative quantities** (steps, distances, energy, exercise minutes): `sum`. A sample
  spanning two buckets is split in proportion to the time in each.
- **Discrete quantities** (heart rate, HRV, body mass...): `avg`, `min`, `max` of the samples
  starting in the bucket. Sound exposure averages as energy (`10·log10(mean(10^(dB/10)))`),
  as Apple does. Temporally weighted types (heart rate) are averaged per sample, each sample
  counting once whatever its duration.
- **Categories** (sleep, stand hours, symptoms...): per value, the samples and the seconds
  they cover inside the bucket. A night's sleep is cut at local midnight: a day bucket holds
  the part of each night after its midnight. Use `interval=hour`, or the samples, to cut
  nights elsewhere.

One request covers at most 366 days or 744 hours. Days are local calendar days of `tz` (23 or
25 hours across a daylight saving time change).

### Counting each moment once (`dedup`)

An Apple Watch and an iPhone carried together both count steps, distance and energy; other
apps may write the same thing again. Summing every sample counts the same steps twice. With
`dedup=priority`, the default, sources are ranked **Apple Watch, then iPhone, then any other
app or device** (from the sample's hardware model, e.g. `Watch6,14`), and a sample counts
only for the part of its time that no sample of a higher-ranked source covers. For
categories, a sample is covered only by a sample of the same value: the watch's sleep stages
leave the phone's *in bed* whole. Apple Health merges sources the same way with its default
source order: the watch wins where both measured, and the phone fills the time the watch was
off the wrist.

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
[`server/sql/views.sql`](https://github.com/leblancfg/healthkit-relay/blob/main/server/sql/views.sql),
which hide samples deleted in Apple Health. Before the query, the server sets:

- `enable_external_access = false`, with `allowed_directories` set to the archive's
  `parquet/`: DuckDB reads that folder and refuses every other file, the network and
  `getenv`;
- `autoinstall_known_extensions = false`, `autoload_known_extensions = false`;
- `memory_limit`, `threads`, `temp_directory = ''` (everything stays in memory),
  `preserve_insertion_order = false`;
- `lock_configuration = true`, which freezes those settings for the rest of the process.

DuckDB runs with an empty environment, as the server's user. Every query filters on the type
and the months it needs (the archive's partitions), so DuckDB opens only those files. The
values in each query come from the server: the type from its catalog, times it parsed,
numbers it checked.
