# Security & privacy

Health data is among the most sensitive data there is. With Health Relay, your data goes from
your iPhone to your server, and only there. Words such as *server address*, *admin token* and
*read token* are defined in the [glossary](glossary.md).

## Where your data goes

- **To your server.** The app connects to one address: the server address you pair it with,
  e.g. `https://health.example.ts.net`. The server answers the app, and the programs you
  give a read token to.
- **To your metrics database, if you choose.** With `-forward-url` set, the server also sends
  numbers (heart rate, steps...) to the address you give, e.g. your VictoriaMetrics.
- **Logs and metrics hold counts.** The app's diagnostic log lists sync steps and counts.
  `/healthz` and `/metrics` give request counts and timestamps. Health values stay in the
  archive.
- **The code is open.** The app and the server are built from
  [this repository](https://github.com/leblancfg/healthkit-relay), and talk only to each
  other and to the addresses above.

## What the app can do

- **Read Apple Health.** It asks HealthKit for read access only; your Apple Health data stays
  as it is.
- **Keep its token on this iPhone.** Its token and the server address are in the iPhone's
  Keychain, for this device only: iCloud Keychain and backups restored to another phone leave
  them out.
- **Wait for the unlock.** While the phone is locked, iOS keeps HealthKit data encrypted; the
  app syncs after the next unlock.

## Credentials

| Credential | Where | Can |
|---|---|---|
| Admin token | your password manager; the server keeps its SHA-256 in `admin.json` (mode 600) | sign in to the setup page; pair, list and revoke paired devices; create read tokens; query; erase any paired device's data |
| A paired device's token | the iPhone's Keychain; the server keeps its SHA-256 in `devices.json` | upload and read the server status, for one paired device; revocable |
| Read token | wherever the program keeps it, e.g. Grafana's settings; the server keeps its SHA-256 in `devices.json` | read health data through the [query API](query-api.md), and nothing else; revocable |
| Setup code | the QR code or its link; the server keeps its SHA-256 in `codes.json` | pair one iPhone, once, within 15 minutes (up to 24 hours with `pair -valid`) |

A SHA-256 is a fingerprint of a token: the server can check a token against it, and nobody
can turn it back into the token.

The admin token is 256 random bits. The server generates it on its first start and prints
it once to its log, with the setup page's address (`docker compose logs relay`,
`journalctl -u healthkit-relay`). The log keeps that line, so whoever can read the server's
logs can read the first admin token. To replace it with one that appears only in your
terminal:

1. Save the admin token from the log in your password manager, and check it signs in to the
   setup page.
2. On the server, run `healthkit-relay admin reset` with the server's `-archive-dir` (with
   Docker Compose: `docker compose exec relay healthkit-relay admin reset`; on NixOS:
   `sudo healthkit-relay-ctl admin reset`).
3. The terminal shows the new admin token. Save it in your password manager in place of the
   old one: the old one stops working at once.
4. Open the setup page. Browsers signed in with the old token are signed out; sign in with
   the new one.

The setup page's sign-in is an HttpOnly, SameSite=Strict cookie that lasts an hour. Its
responses are kept out of caches, send no referrer, and refuse to be shown in a frame.

## Threat model

**Health Relay protects against:**

- *Someone on the network path* reading or altering data: use HTTPS (Tailscale or a reverse
  proxy). Plain HTTP is for trusted home networks only.
- *A client without a token* reading, uploading or deleting data: every endpoint that reads
  or changes data needs a token, and each token does only what its scope allows.
- *A lost or sold phone*: revoke it on the setup page; the admin token and the other paired
  devices keep working.
- *Pairing from outside*: by default, the setup page and pairing answer private networks only
  (your home network, Tailscale).
- *Partial writes and retries*: a batch is stored whole or not at all; a batch sent twice is
  stored once.

**Your responsibility:**

- *The server host.* Anyone with root, or read access to the archive directory, can read
  everything. Use full-disk encryption, keep the host up to date, and restrict who logs in.
- *Backups.* Encrypt them; see [Backups](backups.md).
- *Downstream tools.* Grafana, the metrics database and anything that reads the Parquet
  files need their own access control.
- *The iPhone itself,* and Apple Health.

## Hardening checklist

- [ ] HTTPS for anything beyond a trusted home network; never plain HTTP on the internet.
- [ ] Admin token saved in a password manager, then replaced with `healthkit-relay admin reset`
      so the log no longer holds a working one.
- [ ] `-token-file` from an earlier version removed: once the server has imported it into
      `admin.json`, delete the file and the flag.
- [ ] `-listen 127.0.0.1:8080` behind a reverse proxy or `tailscale serve`.
- [ ] `-setup-from private` (the default), or `off` once your phones are paired.
- [ ] The systemd hardening options from [Linux binary + systemd](setup/linux.md).
- [ ] Full-disk encryption on the server; encrypted off-site backups.
- [ ] One read token per program, so you can revoke one alone.
- [ ] Paired devices and read tokens you no longer use revoked on the setup page.

## Deleting your data

**Erase Server Data and Start Over**, in the app's **Advanced** screen, permanently deletes
everything that iPhone uploaded (Parquet rows, staged rows, de-duplication entries), then
uploads its history again. To delete a paired device's data and keep it from uploading
again, revoke it, then erase its data with the admin token (see
[Pairing](pairing.md#leaving)). To delete everything, stop the server and delete the archive
directory. Delete the data forwarded to a metrics database there too.

## Reporting a vulnerability

Privately, through
[GitHub's private vulnerability reporting](https://github.com/leblancfg/healthkit-relay/security/advisories/new).
See [SECURITY.md](https://github.com/leblancfg/healthkit-relay/blob/main/SECURITY.md).

## Not a medical device

Health Relay copies data. It does not diagnose, treat or monitor any condition, has not been
reviewed by any regulator, and is not a substitute for professional medical advice. Values
may be incomplete, delayed or wrong. See
[DISCLAIMER.md](https://github.com/leblancfg/healthkit-relay/blob/main/DISCLAIMER.md).
