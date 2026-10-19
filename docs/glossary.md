# Glossary

These are the words this guide uses. Each one names one thing, and every page uses the same
word for it.

## Server

The `healthkit-relay` program, running on a computer you keep on: a home server, a NAS, a
Raspberry Pi, a Mac or a small rented server (a VPS, virtual private server). Your iPhone
sends its Health data to the server, and the server keeps it in the [archive](#archive).

Example: in the [Quick start](quickstart.md), `docker compose up -d` starts the server in
Docker. The [setup guides](setup/index.md) show the other ways to run it.

## Server address

The address your iPhone uses to reach the server. You set it with the `-public-url` flag, or
the `HKR_PUBLIC_URL` setting, and the server puts it in every [setup code](#setup-code).

Examples:

- `http://192.168.1.10:8080`: the server on your home Wi-Fi, at the IP address
  `192.168.1.10`, port `8080`.
- `https://health.example.ts.net`: the server over Tailscale, with HTTPS (see
  [Networking & HTTPS](networking.md)).

## Setup page

The server's web page for pairing iPhones and creating read tokens. Its address is the server
address followed by `/setup`. You sign in to it with the [admin token](#admin-token).

Example: `http://192.168.1.10:8080/setup`, opened in a browser on a computer at home.

## Admin token

The password of your server. The server makes it on its first start and prints it once, in
its log. It signs in to the setup page and manages tokens. Save it in a password manager.
`healthkit-relay admin reset` makes a new one, and the old one stops working at once.

Example: the first start of the server prints a line like this one in its log:

```text
    Admin token: BBvGW16otZlTAfIc7RbrNmMVd7PXBVU8skBFvHZhO_4
```

## Setup code

A one-time code that pairs one iPhone. The setup page shows it as a QR code, and
`healthkit-relay pair` prints one in the terminal. A setup code works once and expires after
15 minutes.

Example: the code `PONKKR6RFXEX5WUCJQI6EBNX`. The QR code holds it as a link that opens the
app, with the server address:

```text
healthrelay://pair?code=PONKKR6RFXEX5WUCJQI6EBNX&server=http%3A%2F%2F192.168.1.10%3A8080
```

## Paired device

An iPhone that has used a setup code. Each paired device gets a token of its own, kept in the
iPhone's Keychain, that uploads its Health data and does nothing else. The setup page lists
paired devices under **Paired devices**. Revoking one stops it at once.

Example: `healthkit-relay devices` lists a paired device as
`3f9a1c2e  iPhone  iPhone16,1  upload  2026-10-19 14:02  just now`.

## Read token

A token that lets a dashboard, a script or an AI agent read your data through the
[query API](#query-api), and do nothing else. You create one on the setup page under **Read
tokens**, or with `healthkit-relay token create -name grafana`. The server shows it once.

Example: a read token named `grafana`, pasted into Grafana as
`Authorization: Bearer ifg0UV7nzyvZmNFG944ioBl-Gy1ubAxxUThIJ52hV9I`.

## Archive

The folder where the server keeps your data, as Parquet files (a file format for tables
that DuckDB, Python and many other tools read). You set it with `-archive-dir`. It also
holds the server's own files: `admin.json`, `devices.json` and `codes.json`. Back it up (see
[Backups](backups.md)).

Examples: `./data` next to `docker-compose.yml` with Docker Compose;
`/var/lib/healthkit-relay` with systemd or NixOS. See
[Data model & querying](data.md#storage) for what is inside.

## Query API

The read-only web API (application programming interface: a set of addresses that programs
call to get data) under `/v1/types`. It answers with your data as JSON, for dashboards,
scripts and AI agents. It is described at `/openapi.json` and shown as a web page at `/docs`.
You turn it on with `-query-duckdb`; it takes a read token. See [Query API](query-api.md).

Example: your steps per day in October 2026, in Toronto time, with the read token in
`$TOKEN`:

```sh
curl -H "Authorization: Bearer $TOKEN" \
  "http://192.168.1.10:8080/v1/types/HKQuantityTypeIdentifierStepCount/aggregates?tz=America/Toronto&start=2026-10-01&end=2026-11-01"
```
