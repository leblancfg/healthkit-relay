# Security & privacy

Health data is among the most sensitive data there is. Health Relay's stance is simple: it
goes from your phone to your server, and nowhere else.

## What never leaves your server

- **No cloud, no account.** There is no Health Relay service. The app's only network
  destination is the server address you pair it with.
- **No analytics, crash reporting or third-party SDKs** in the app or the server.
- **No health data in logs or metrics.** The app's diagnostic log has steps and counts, not
  values. `/healthz` and `/metrics` expose request counts and timestamps only.
- **Forwarding is opt-in.** Numbers go to a metrics database only if you set `-forward-url`,
  and only to the address you give.

## What the app can do

- **Read only.** It asks HealthKit for read access and never writes to Apple Health.
- Its token and the server address are in the iPhone Keychain, for this device only (not
  synced to iCloud Keychain, not in backups restored to another phone).
- While the phone is locked, HealthKit data is encrypted and unreadable; the app syncs after
  the next unlock.

## Credentials

| Credential | Where | Can |
|---|---|---|
| Server token | a file on the server (mode 600) | everything: pair, list and revoke devices, upload |
| Device token | the iPhone's Keychain; the server stores only its SHA-256 | upload and read the server status, for one device; revocable |
| Read token | wherever the query client keeps it; the server stores only its SHA-256 | read health data through the [query API](query-api.md), nothing else; revocable |
| Setup code | the QR code or link | pair once, within 15 minutes (up to a day with `-valid`) |

The server token is read from a file, never from a command-line value, so it does not show
in process listings. The setup page's sign-in is an HttpOnly, SameSite=Strict cookie that
lasts an hour; its responses are not cached, send no referrer and cannot be framed.

## Threat model

**Designed to protect against:**

- *Someone on the network path* reading or altering data: use HTTPS (Tailscale or a reverse
  proxy). Plain HTTP is for trusted LANs only.
- *An unauthenticated client* reading, uploading or deleting data: every data endpoint needs
  a token; there is no endpoint that returns health data.
- *A lost or sold phone*: revoke its device token; the server token is unaffected.
- *Pairing from outside*: the setup page and pairing answer private networks only by default.
- *Partial writes and retries*: a batch is stored whole or not at all; duplicates are ignored.

**Out of scope, your responsibility:**

- *The server host.* Anyone with root, or read access to the data directory, can read
  everything. Use full-disk encryption, keep the host patched, and restrict who logs in.
- *Backups.* Encrypt them; see [Backups](backups.md).
- *Downstream tools.* Grafana, the metrics database and anything that reads the Parquet
  files need their own access control.
- *A compromised iPhone,* or Apple Health itself.

## Hardening checklist

- [ ] HTTPS for anything beyond a trusted LAN; never plain HTTP on the internet.
- [ ] Server token file mode 600, owned by the service user.
- [ ] `-listen 127.0.0.1:8080` behind a proxy or `tailscale serve`.
- [ ] `-setup-from private` (default), or `off` once your phones are paired.
- [ ] The systemd hardening options from [Linux binary + systemd](setup/linux.md).
- [ ] Full-disk encryption on the server; encrypted off-site backups.
- [ ] Revoke devices you no longer use.

## Deleting your data

**Erase Server Data and Start Over** in the app permanently deletes everything that iPhone
uploaded (Parquet rows, staged rows, de-duplication entries), then uploads its history again. To delete
a device's data without a new upload, revoke it and erase with the server token (see
[Pairing](pairing.md#leaving)). To delete everything, stop the server and delete the data
directory. Data already forwarded to a metrics database must be deleted there
too.

## Reporting a vulnerability

Privately, through
[GitHub's private vulnerability reporting](https://github.com/leblancfg/healthkit-relay/security/advisories/new).
See [SECURITY.md](https://github.com/leblancfg/healthkit-relay/blob/main/SECURITY.md).

## Not a medical device

Health Relay copies data. It does not diagnose, treat or monitor any condition, has not been
reviewed by any regulator, and is not a substitute for professional medical advice. Values
may be incomplete, delayed or wrong. See
[DISCLAIMER.md](https://github.com/leblancfg/healthkit-relay/blob/main/DISCLAIMER.md).
