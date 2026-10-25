# Adversarial review log

Two rounds of a strict App Review and security review (a separate model, maximum
reasoning) before the first submission. Each finding, and what was done.

## Round 1 (on 18900c3)

| # | Finding | Done |
|---|---|---|
| 1 | The server ID is public, so a crafted setup link to another server that echoes it skipped the server-change question and received the old token | Links skip the question only for the current address or the https address the paired server itself named in an authenticated answer (PR #11) |
| 2 | Pairing and upload requests could follow redirects to other servers | Every request with the token refuses redirects; see round 2, #8 |
| 3 | The server's downloads (repo, releases, image) are private, so nobody can install a server | Owner's decision: publish before submitting. Quickstart and README now install the app from the App Store |
| 4 | "Data Not Collected" while the developer's demo server kept uploads for an hour | Demo keeps nothing (round 2, #3) |

## Round 2 (on 508550d)

| # | Sev. | Finding | Done |
|---|---|---|---|
| 1 | Blocking | A status answer from the old server, arriving after a new pairing, could hand the new server's token to the old server's preferred address | Answers are dropped when the settings changed while they were on their way; the handoff re-checks before every request |
| 2 | Blocking | Disconnect left a running sync and system uploads going | Disconnect stops the sync and cancels the system's uploads; each batch checks the settings are still the ones it started with |
| 3 | Blocking | The demo server kept device entries, counts and log lines | Stateless demo: signed tokens (30 days), no device list, aggregate count only, no per-request logs |
| 4 | Major | Disconnect then pairing with another server kept the old sync positions | A new pairing with any other server, or after a disconnect, resets them; Disconnect drops pending uploads |
| 5 | Major | A route, ECG or series read cut short is uploaded with an error field and its anchor moves | Kept as designed for 1.0: the record carries the error, and the server keeps the sample. Follow-up: retry incomplete children |
| 6 | Major | Encoding a large page can use more memory than one batch | Not seen on real histories (the owner's iPhone, 6.6 million records, synced with it); follow-up: stream records into batches |
| 7 | Major | Plain HTTP was allowed to any host | http:// only for private, link-local, Tailscale IPs and .local or single-label names; the manual form says others on the network can read it |
| 8 | Major | The background session follows redirects before the app can check | iOS gives apps no say over redirects in background sessions; an answer from another origin now counts as not sent. Only the user's own server can redirect over HTTPS; on LAN HTTP an attacker can already read the traffic. Forwarding refuses redirects |
| 9 | Major | The only in-app erase starts uploading again | Erase Server Data and Disconnect; the policy describes both, and what the erase covers |
| 10 | Major | The policy said deleting the app removes its token (Keychain items survive) | A new install clears what an earlier one left; the policy says so and how to revoke |
| 11 | Major | Docs still advertised Health Records | Docs and listing say clinical records are left out of the App Store app |
| 12 | Major | 5.1.1(ix): individual seller of an app handling sensitive data | Review notes explain it: no service, no data held by the developer. Follow-up if Apple asks: submit from an incorporated entity (D-U-N-S) |
| 13 | Major | 4.2.3(i): the app needs a separately installed server | Review notes explain the self-hosted design; the demo server stands in during review |
| 14 | Major | The demo's byte quota was checked only before reading | Body capped to what is left of the quota, 4 uploads at once at most |
| 15 | Major | Demo pairings grew devices.json without bound | No device list in the demo |
| 16 | Major | A shared quota could block the reviewer's first sync | Quota resets hourly; a reviewer's test device holds little data; 429 carries Retry-After and the app retries at the next sync |
| 17 | Major | Error paths logged parts of rejected batches | The demo logs nothing per request; a self-hosted server logs to its owner |
| 18 | Major | No per-device storage budget on ordinary servers | Self-hosted: the owner pairs only their devices. Follow-up |
| 19 | Major | Global de-duplication lets one paired device shadow another's v1 copy | Devices are the owner's own; follow-up to scope by uploader |
| 20 | Major | Repeated erases of an empty device held the archive locks | An erase with nothing stored returns at once |
| 21 | Major | Setup forms without Fetch Metadata had no origin check | Origin must be the page's own |
| 22 | Major | Renames were not synced with their directory | State files sync the directory after the rename |
| 23 | Major | Compose published Grafana (first login admin/admin) on every interface | Bound to 127.0.0.1 by default; docs say how to open it after setting a password |
| 24 | Major | A custom URL scheme can be claimed by another app | Setup codes are single-use and expire in 15 minutes; follow-up: universal links |
| 25 | Minor | The admin token is printed once in the log; typed in a plain text field | Documented rotation (security.md); the field mostly takes short setup codes |
| 26 | Minor | "Private network" trusts a local proxy's X-Forwarded-For | Documented; Funnel requests are recognised by their header |
| 27 | Minor | Review notes promised a video; "Sign-in required" said No | The screenshot run records the video; sign-in information carries the address and code |
| 28 | Minor | Demo mode did not refuse a real archive | The demo no longer writes or erases the archive |

## Round 3 (on 5a4e13d)

Round 2's fixes verified; "Data Not Collected" holds for the stateless demo; the accepted
items' justifications hold.

| # | Sev. | Finding | Done |
|---|---|---|---|
| 1 | Blocking | A late erase failure restored the old credentials after a Disconnect or a new pairing | A connection generation changes at every connect, disconnect and erase; an awaited setup, token upgrade or erase saves or restores only if it did not change; Disconnect and Connect wait while an erase runs |
| 2 | Major | `010.010.010.010` passed the local-address check (Darwin reads it as 8.8.8.8) | IPv4 only in plain form: four decimal numbers without leading zeros |
| 3 | Major | A late first-page confirmation from the old server could move the new server's reset positions | Each position an upload carries names its connection; it moves only on that connection, checked after the answer and before applying; a new pairing stops the old work before resetting |
| 4 | Minor | A late status error could mark the new server revoked or unreachable | The error paths check the connection too |

## Round 4 (on 23ca6cc)

Round 3's #2 and #4 fixed; #1 and #3 partly. Prior accepted items stay accepted.

| # | Sev. | Finding | Done |
|---|---|---|---|
| 1 | Blocking | A setup queued behind another before a Disconnect connected afterwards; a Disconnect during the position reset left the screen connected | A lifecycle epoch, changed only by Disconnect and erase, is taken when a setup is accepted and checked before it pairs, after every answer and after the reset; Disconnect and Connect wait while a setup runs |
| 2 | Blocking | The address handoff's last check and save could straddle a Disconnect | The handoff only verifies; AppModel.adoptAddress checks and saves in one main-actor step, the actor Disconnect runs on |
| 3 | Major | Empty pages, activity summaries, effort relationships, app 0.7 orphan commits and same-server erases could still move a reset position | Every position change goes through one owner check: the connection and a sync epoch that every reset renews; commits without an owner move nothing (the page is sent again, the server keeps one copy) |
| 4 | Blocking | The Funnel demo address was unreachable from the internet | It was being retired: by owner decision (11:45) the demo moved to a Cloudflare Worker at https://health-relay-demo.leblancfg.com (another session; homelab ed102a7), which answers; the notes and listing move to it with that work |
