---
description: >-
  Fix common Health Relay problems: the admin token, pairing errors, background sync on the iPhone, missing data types, Grafana and the query API.
---

# Troubleshooting

Each problem below says what you see, why it happens, what to do, and how to check it worked.
Words such as *server address*, *setup page* and *admin token* are defined in the
[glossary](glossary.md).

Start with the two logs:

- the server's log: `docker compose logs relay` (Docker Compose), `journalctl -u
  healthkit-relay` (Linux and NixOS), or the terminal or log file it runs in;
- the app's diagnostic log: in the app, tap the gear, then **Share Diagnostic Log**.

Both list steps and counts; health values stay in the archive. Remove your addresses before
you share them.

## Server

### Where is the admin token?

**What you see.** The setup page asks for the **Admin token**, and you do not have it.

**Why.** The server prints the admin token once, in its log, on its very first start, and
keeps only its fingerprint (`admin.json`).

**What to do.**

1. Look in the log of the first start: `docker compose logs relay | grep -i "admin token"`,
   or `journalctl -u healthkit-relay | grep -i "admin token"`.
2. If the log no longer has it, make a new one on the server:

    ```sh
    docker compose exec relay healthkit-relay admin reset        # Docker Compose
    sudo healthkit-relay-ctl admin reset                         # NixOS
    sudo healthkit-relay admin reset -archive-dir /var/lib/healthkit-relay   # Linux binary
    ```

    The terminal shows the new admin token. The old one stops working at once.

3. Save it in your password manager.

**Check.** Paste it on the setup page and click **Sign in**: the page shows
**Pair your iPhone**.

### A command says the archive directory "belongs to uid"

**What you see.** `pair`, `devices`, `revoke`, `token create` or `admin reset` stops with a
message that the archive directory belongs to a uid (a user number), e.g. `belongs to uid
61234`.

**Why.** These commands change the server's state files, so they run as the user that owns
the archive directory (the server's user), or as root.

**What to do.** Run the command with `sudo`, or the way your setup provides, with the
server's `-archive-dir` (see [Commands](setup/index.md#commands)):
`sudo healthkit-relay pair -archive-dir /var/lib/healthkit-relay`,
`docker compose exec relay healthkit-relay pair`, or `sudo healthkit-relay-ctl pair` on NixOS.

**Check.** The command prints its result: a QR code for `pair`, a table for `devices`.

### The server stops at start with a `-token-file` error

**What you see.** The server exits right after it starts, and the last log line names
`-token-file`: the file cannot be read, or its token is shorter than 16 characters.

**Why.** `-token-file` (`HKR_TOKEN_FILE`) is a setting from earlier versions, where you made
the server token by hand. At start, the server imports that token as the admin token. A
missing file is skipped; a file the server's user cannot read, or a token under 16
characters, stops it.

**What to do.**

1. If the server has started with this version before, `admin.json` already holds the admin
   token: remove `-token-file` (or `HKR_TOKEN_FILE`) from the settings and delete the file.
2. Otherwise, make the file readable by the server's user, or put the old token back in it,
   and start the server once so it imports it. Then remove the setting and delete the file.

**Check.** The server keeps running, and its log has no `-token-file` warning.

### Other computers cannot connect

**What you see.** The server answers on the server itself (`curl http://127.0.0.1:8080/healthz`
prints `ok`), but a browser or `curl` on another computer gets *Failed to connect* or waits
until it gives up.

**Why.** The binary, the systemd unit and the NixOS module listen on `127.0.0.1:8080` by
default: only the server itself reaches that address. The Docker image listens on every
network interface.

**What to do.**

1. Listen on every interface: `HKR_LISTEN=0.0.0.0:8080` in `/etc/healthkit-relay/env`
   (Linux binary), or `listen = "0.0.0.0:8080";` and `openFirewall = true;` on NixOS. Restart
   the server.
2. Open port 8080 in the host's firewall: `sudo ufw allow 8080/tcp`, or
   `sudo firewall-cmd --add-port=8080/tcp`.

**Check.** From another computer, `curl http://192.168.1.10:8080/healthz` (with your server's
address) prints `ok`.

### "permission denied" on the data directory (Docker)

**What you see.** The container stops, and `docker compose logs relay` shows `permission
denied` on `/data`.

**Why.** `./data` belongs to another user than the one the container runs as.

**What to do.** See [Docker & Compose](setup/docker.md#permissions).

**Check.** `docker compose ps` shows `relay` as `Up`.

### Uploads fail with 413

**What you see.** The app's status names `413`.

**Why.** A batch was larger than the server's `-max-body-bytes` (32 MB by default), or than
your reverse proxy allows.

**What to do.** Allow 32 MB request bodies at the reverse proxy, e.g.
`client_max_body_size 32m;` in nginx, and reload it.

**Check.** In the app, tap **Sync Now**: it ends with **Up to date**.

## Pairing

### The setup page shows "Set the server address"

**What you see.** The setup page shows **Set the server address** instead of a QR code.

**Why.** The page was opened at an address an iPhone cannot reach, such as `127.0.0.1` or
`localhost`, and the server found no other address it answers on.

**What to do.** One of:

- open the setup page with the address the iPhone uses, e.g.
  `http://192.168.1.10:8080/setup`;
- start the server with `-public-url` (`HKR_PUBLIC_URL`) set to the server address;
- start the server with `-listen 0.0.0.0:8080`, so the page can offer the computer's network
  addresses.

**Check.** The page shows **Pair your iPhone** and a QR code.

### The setup page answers 403, or a message about private networks

**What you see.** Opening `/setup` gives a short page about private networks, with status
403.

**Why.** By default (`-setup-from private`), the setup page and pairing answer private
networks only: your home network and Tailscale. Behind a reverse proxy on another host, the
server sees the proxy's address, which may be public.

**What to do.**

- From outside your home, pair over a VPN such as Tailscale, or at home.
- Behind a reverse proxy, run the proxy on the same host as the server, or pair with
  `healthkit-relay pair` on the server.

**Check.** The setup page asks for the **Admin token**.

### The setup code expired or was already used

**What you see.** The app or the setup page says the setup code expired or was already used.

**Why.** A setup code pairs one iPhone, once, within 15 minutes.

**What to do.** Make a new one: reload the setup page, or run `healthkit-relay pair` again.
To send the setup code's link to a phone that will open it later, make one that lasts
longer: `healthkit-relay pair -valid 2h`.

**Check.** The app shows the server's address and asks you to **Connect**.

### The Camera shows no "Open in Health Relay"

**What you see.** The iPhone's Camera sees the QR code but offers no **Open in Health Relay**
button.

**Why.** The setup code's QR code opens the Health Relay app, and the app is not installed
on that iPhone.

**What to do.** Install the app, open it once, then scan the code again.

**Check.** The Camera shows **Open in Health Relay**.

## Syncing

### The first sync takes hours

**What you see.** The app shows **Syncing** for a long time after pairing.

**Why.** The first sync uploads your whole history, which can be hundreds of thousands of
samples.

**What to do.** Keep the app open and the phone unlocked and charging. If the sync stops, it
continues where it stopped. On iOS 26 and later, a first sync, or a **Sync Now** after hours
without a sync, also goes on when you switch to another app: iOS shows its progress in a Live
Activity, where you can stop it. Locking the iPhone pauses it, since Health data is locked
then; it goes on after you unlock.

**Check.** The app shows **Up to date**.

### No syncs in the background

**What you see.** The app syncs when you open it, and at no other time.

**Why.** Health Relay syncs in the background several times a day; open the app to sync
right away. iOS decides when to wake the app: after new Health data arrives, at its
background app refresh, while the iPhone charges, and for about 30 seconds after you leave
the app. Low Power Mode, closing the app from the app switcher, and Background App Refresh
turned off all hold these wakes back. iPhone keeps Health data locked while the phone is
locked, so a wake while it is locked sends nothing new: it notes a sync as owed, and the
first wake after an unlock (or opening the app) sends everything since. With Tailscale, the
iPhone reaches the server in the background only with VPN On Demand.

**What to do.**

1. Turn on Background App Refresh: Settings › General › Background App Refresh, for Health
   Relay.
2. Leave the app in the app switcher, and Low Power Mode off.
3. With Tailscale, turn on VPN On Demand in the Tailscale app.

**Check.** The server's log says what started each batch, e.g.
`batch v2: … trigger observer, app background`, and
`healthkit_relay_batches_total{trigger}` counts them. Triggers other than `foreground` mean
iOS woke the app.

From app 0.6.2 and server 0.6.2, the app also sends a heartbeat at the end of each background
wake, even when it could read or send nothing. The server logs one line per heartbeat:

```text
heartbeat: device eae02a2e, app 0.6.4 (11), trigger refresh, background, protected data unavailable, outcome locked, on battery, 0 uploaded in 0.2s, 28s background time left, owes a sync (locked), process 5h0m0s old; wakes: observer 3 (last 10-20 08:12:40), refresh 5 (last 10-20 13:05:11)
```

- The trigger says what woke the app: `observer` (new Health data), `refresh` (background
  app refresh), `processing` (the processing task while charging), `urlsession` (an upload
  left under way finished, from app 0.7.0), `unlock` (the first unlock after a skipped
  sync), `backgrounded` (you left the app), `continued` (a sync you started that went on
  outside the app, iOS 26) and `retry` (after a failure). Server 0.7.0 counts each under
  its own name; older servers count the new ones as `unknown`.
- `protected data unavailable` and `outcome locked`: the iPhone was locked; the sync waits
  for a wake after an unlock.
- `plugged in` or `on battery`, then `low power mode` and `background app refresh denied`
  when they apply (app 0.6.4 and server 0.6.4): what iOS weighs before it wakes the app.
  Wakes that come only while `plugged in` mean iOS is holding the app's background work for
  the charger.
- `outcome upload failed` or `cancelled`, or a `last error`: the app read Health but could
  not send everything in the time iOS gave it. The next wake sends the rest.
- No heartbeat at all for hours: iOS did not wake the app. See the steps above.

`healthkit_relay_last_heartbeat_timestamp_seconds{trigger}` and
`healthkit_relay_last_batch_timestamp_seconds{trigger}` give the time of the last of each, and
refused tokens are logged with the client's address and user agent (`refused: POST
/v1/samples from …, 401, …`).

### The app shows the server as not reachable on a home network

**What you see.** The app shows **Not reachable right now** while the iPhone is on the same
Wi-Fi as the server.

**Why.** The app needs iOS's Local Network permission to reach addresses on your home
network. Also, iOS allows plain `http://` only to IP addresses (`192.168.1.10`) and `.local`
names; other names need `https://`.

**What to do.**

1. Allow it: Settings › Privacy & Security › Local Network › Health Relay.
2. Check the server address in the app (the gear, **Server address**). With plain `http://`, use
   an IP address or a `.local` name, or set up HTTPS: see
   [Networking & HTTPS](networking.md).

**Check.** In the app, tap **Sync Now**: it ends with **Up to date**.

### The app shows "Disconnected"

**What you see.** The app shows **Disconnected** and asks you to scan a setup code from the
setup page.

**Why.** The server no longer knows this iPhone's token: the paired device was revoked, or
the server's archive directory was replaced (for example by an older backup or a new
install).

**What to do.** [Pair the iPhone again](pairing.md).

**Check.** The app shows **Up to date**, and the setup page lists the iPhone under
**Paired devices**.

### A data type is missing

**What you see.** A type you have in Apple Health is not on the server.

**Why.** Health access is granted type by type. Some types are granted item by item
(vision prescriptions, medications; in builds of your own, clinical documents and Health
Records). Types your iOS
version does not have are skipped.

**What to do.**

1. In the app, tap the gear, then **Data types read**: it lists what the app reads.
2. For the types granted item by item, tap each one under **Shared item by item** and pick
   the items to share.
3. After an app update adds types, the app asks for Health access again on its own: allow
   them.

**Check.** After the next sync, `GET /v1/types?present=true` in the
[query API](query-api.md), or a DuckDB query, lists the type.

### Data is in the archive but not in Grafana

**What you see.** DuckDB or the query API shows the data; Grafana shows nothing, or only
the last month.

**Why.** The server forwards numbers only, and only new ones: what it stores after
`-forward-url` is set. A metrics database with a short retention drops older history
(VictoriaMetrics keeps one month by default).

**What to do.**

1. Check `healthkit_relay_forward_errors_total` on `/metrics` and the server's log for
   forwarding errors.
2. Give the metrics database a long retention, e.g. `-retentionPeriod=100y` for
   VictoriaMetrics.
3. To forward the history the server already holds, see [Grafana](grafana.md#with-docker-compose).

**Check.** In Grafana, **Explore** shows `healthkit_heart_rate` back to your first data.

## Query API

### `/docs` answers 404

**What you see.** `http://192.168.1.10:8080/docs` shows `404 page not found`.

**Why.** The query API is off: the server starts it only with `-query-duckdb`.

**What to do.** [Turn it on](query-api.md#turn-it-on).

**Check.** `/docs` shows **Health Relay query API**, and the server's log has a line that
starts with `query API on:`.

### A query answers 401 or 403

**What you see.** `{"error":"unauthorized"}` (status 401), or an error about the kind of
token (status 403).

**Why.** 401: the server does not know the token. It was mistyped, cut short, or revoked.
403: the token is a paired device's token, which uploads and does not query.

**What to do.** [Create a read token](query-api.md#get-a-read-token) and paste the whole of
it, after `Authorization: Bearer `.

**Check.** `curl -H "Authorization: Bearer <token>" http://192.168.1.10:8080/v1/types`
answers `{"types":[...`.

### A query answers 400 "unknown type"

**What you see.** e.g.
`{"error":"unknown type HKQuantityTypeIdentifierSteps; did you mean HKQuantityTypeIdentifierStepCount?"}`.

**Why.** The type in the address is not one of HealthKit's identifiers.

**What to do.** Use the name the message suggests, or pick one from `GET /v1/types`.

**Check.** The same request answers with data.

### Today's data is missing from queries

**What you see.** The app is **Up to date**, but the query API or DuckDB lacks the last
hour.

**Why.** The server writes new samples to Parquet at the hourly flush; the query API and
DuckDB read Parquet.

**What to do.** Wait for the next flush (at most an hour, `-flush-interval`).

**Check.** The same query, an hour later, includes the samples.

### A query answers 422, 503 or 504

**What you see.** An `error` with status 422 (the answer is too large), 503 (too many queries
at once) or 504 (the query took too long).

**Why.** The server bounds each query: `-query-max-rows`, `-query-concurrency` and
`-query-timeout` (see [Settings](query-api.md#settings)).

**What to do.** 422 and 504: ask for a shorter range, a smaller `limit`, or leave out `raw`.
503: try again after a few seconds.

**Check.** The query answers `200` with data.

### The log says the test query failed

**What you see.** At start, the server's log has a line with `the test query failed`, and
every query answers 500. Or the server stops at start with an error that names
`-query-duckdb`.

**Why.** `duckdb` could not run or could not read the archive. The end of the log line
gives the reason, e.g. a DuckDB version before 1.2, or a folder the server's user cannot
read. When the server stops at start, `-query-duckdb` names a file that is not there.

**What to do.** Install `duckdb` 1.2 or newer, check that the path in `-query-duckdb` is
right, and that `-query-archive-dir`, if set, is readable by the server's user. Restart the
server.

**Check.** The log shows `query API on:` with no failed test query after it.

## Still stuck?

Open an [issue](https://github.com/leblancfg/healthkit-relay/issues/new/choose) with the
versions, how the server runs and the relevant log lines. Leave out health data and tokens.
