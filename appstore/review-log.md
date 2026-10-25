# Adversarial review log

Rounds of a strict App Review and security review (a separate model, maximum reasoning)
before the first submission. Each finding, and what was done.

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

## Round 5 (on 24dd293)

Round 4's lifecycle epoch, main-actor adoption and position owner checks verified at every
write site; no new HealthKit, background-mode, privacy-manifest or export-compliance defect;
the demo Worker keeps aggregate counters only, with request logs off. Prior accepted items stay
accepted.

| # | Sev. | Finding | Done |
|---|---|---|---|
| 1 | Blocking | Address and token were two Keychain items: a background heartbeat could read one server's address and another's token | One Keychain item holds both, read and replaced in one step (update in place); the two items of earlier apps are read until the next save |
| 2 | Blocking | The address handoff noted the preferred address off the main actor, where it could outlive a new pairing and let a link skip the question | The handoff changes nothing the app trusts; AppModel.noteHandoff notes it on the main actor if the connection is unchanged and no setup or erase runs; a preferred address counts only while the app is connected where the server named it |
| 3 | Blocking | A setup link opened during Erase Server Data and Disconnect paired again, and the erase then skipped the disconnect | Erase and setup exclude each other in the model: setups are refused while erasing, erases while a setup runs or waits; syncs wait for the erase; Erase and Start Over sends nothing again after a disconnect and cancels the system's uploads first |
| 4 | Major | An upload admitted before an erase could store its batch after it | The server counts erases per device; an upload notes the count when admitted and is refused (409) if an erase finished since; the app waits until its uploads are cancelled before asking for the erase |
| 5 | Major | A handoff answer arriving during a re-pairing could save the old token and discard the rotated one | No move to a preferred address while a setup runs or waits, or while erasing |
| 6 | Major | A unit test still expected the uploader to add the connection to a commit | The test gives the commit its owner and checks it is carried unchanged through every retry |
| 7 | Minor | Concurrent demo uploads each read the same remaining quota | Kept: the overshoot is bounded (4 uploads at once, each capped to the body limit), the quota guards the free Cloudflare plan, not privacy, and nothing is stored |
| 8 | Minor | The demo setup page said uploads were erased hourly | It says uploads are checked and dropped at once, and only hourly counts are kept (Worker and Go demo mode) |

## Round 6 (on 5a05e27)

Round 5's #1, #2, #5, #6 and #8 verified; the new Swift checked as a Swift 5 compiler would
(no type, isolation, overload or access error found); the server barrier's lock order holds.

| # | Sev. | Finding | Done |
|---|---|---|---|
| 1 | Blocking | The launch-time admin-token upgrade could still pair during Erase Server Data and Disconnect | The upgrade, like any setup, does not run while erasing (checked before it queues and at its turn), and opening the app during an erase does nothing |
| 2 | Major | Cancelling the app's uploads does not wait for them to stop; an upload buffered on the way could reach the server after the erase and be stored | Erase Server Data and Disconnect asks the server to revoke the device's token in the same step (`?disconnect=1`, under the same lock as the erase): a late upload gets 401. For Erase and Start Over a late copy of old data is harmless: the app sends its whole history again and the server keeps one copy |

## Round 7 (on 6b77d32)

Round 6's #1 verified; no lock-order deadlock between the archive and the device list; a
revoke failure after the erase answers 500 and the app keeps its credentials. Demo answers
unchanged.

| # | Sev. | Finding | Done |
|---|---|---|---|
| 1 | Major | An upload checked before the revoke but noting the erase count after the erase could still be stored | The upload notes the count before its token is checked: an erase finishing after that voids it (409), and one finishing before it revoked the token (401). The count is server-wide: an erase voids every upload under way, which the app sends again |
| 2 | Major | The app took an erase without revocation (a server before 1.0, the admin token) as a disconnect | The answer says `"disconnected": true` when nothing more sent with the token is stored (revoked, or a demo server); otherwise the app still disconnects (the data is erased) and says to revoke the iPhone, or change the admin token, on the server |
