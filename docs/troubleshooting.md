# Troubleshooting

Start with the two logs: the server's (`journalctl -u healthkit-relay`, `docker compose
logs`) and the app's diagnostic log (Advanced, the gear). Neither contains health values, but
scrub addresses before sharing them.

## Server

**It exits at start with a token error.** The token file must exist, be readable by the
service user and hold at least 16 characters. Make one with
`head -c 32 /dev/urandom | base64 > token`.

**Other machines cannot connect.** The default is `-listen 127.0.0.1:8080`, which only the
server itself can reach. Use `0.0.0.0:8080` for LAN access (always, inside a container), and
check the host firewall (`ufw allow 8080/tcp`, `firewall-cmd --add-port=8080/tcp`).

**"permission denied" on the data directory (Docker).** See
[Docker & Compose](setup/docker.md#permissions).

**Uploads fail with 413.** A batch was larger than `-max-body-bytes`, or than your reverse
proxy allows. Allow 32 MB request bodies at the proxy (`client_max_body_size 32m;` for nginx).

## Pairing

**The setup page shows no QR code.** The server found no address a phone could use (it never
offers `127.0.0.1` or `localhost`). Open the page with the address the phone will use, or set
`-public-url`.

**The setup page answers 403 or does not load.** `/setup` answers private networks only by
default. From the internet, pair over a VPN or from home. Behind a reverse proxy on another
host, the server sees the proxy's address; put the proxy on the same host, or use
`healthkit-relay pair` on the server.

**"Code expired or already used".** Codes work once, for 15 minutes, and a server restart
voids them. Make a new one. For a link sent to the phone, use `pair -valid 2h`.

**Scanning the code opens Safari, not the app.** The app is not installed on that phone, or
was installed under another bundle ID. Install it, then scan again.

## Syncing

**Stuck on the first sync.** The first sync uploads your whole history, which can be
hundreds of thousands of samples. Keep the app open and the phone unlocked and charging; it
resumes where it stopped if interrupted.

**No background syncs.** iOS decides when to wake the app, about hourly at best. Low Power
Mode, force-quitting the app from the app switcher, and Background App Refresh being off
(Settings › General › Background App Refresh) all stop it. Opening the app always syncs. With
Tailscale, turn on VPN On Demand so the server is reachable in the background.

**"Could not connect" on a LAN.** Allow Local Network access (Settings › Privacy & Security ›
Local Network › Health Relay). Plain HTTP works only with IP addresses and `.local` names;
anything else needs HTTPS. See [Networking & HTTPS](networking.md).

**401 / asked to set up again.** The device was revoked, or the server's data directory was
replaced. Pair again.

**A data type is missing.** Check Advanced › *Data types read*. If Health access was
granted before an app update added types, the app asks again by itself. Some types are
granted item by item (vision prescriptions, medications, clinical documents, Health
Records): allow them under Advanced. Types your iOS version does not have are skipped.

**Data appears in the archive but not in Grafana.** Only numeric samples are forwarded, and
only new ones. Check `healthkit_relay_forward_errors_total` and the server log, and the
retention of your metrics database: a one-month retention drops older history from the
first sync.

## Still stuck?

Open an [issue](https://github.com/leblancfg/healthkit-relay/issues/new/choose) with the
versions, how the server runs and the relevant log lines. Never include health data or
tokens.
