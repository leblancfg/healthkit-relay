# App Store listing: Health Relay 1.0.0

Drafts for App Store Connect, field by field, with the limits App Store Connect enforces.
Kept out of `docs/` so the documentation site does not publish them. Review notes are in
[review-notes.md](review-notes.md), screenshots and the demo video in
[screenshots.md](screenshots.md).

## App information

| Field | Value |
|---|---|
| Name (30) | Health Relay |
| Subtitle (30) | Health data to your own server |
| Bundle ID | com.leblancfg.healthkitrelay |
| SKU | health-relay |
| Primary language | English (U.S.) |
| Primary category | Health & Fitness |
| Secondary category | Utilities |
| Content rights | Contains no third-party content |
| Age rating | 4+ (answers below) |
| Price | Free (tier 0), all territories |
| Copyright | 2026 François Leblanc |
| Seller / developer name | François Leblanc (individual, team XFUXB8URRT) |
| Privacy policy URL | https://leblancfg.com/healthkit-relay/privacy/ |
| Support URL | https://leblancfg.com/healthkit-relay/support/ |
| Marketing URL | https://leblancfg.com/healthkit-relay/ |

## Version 1.0.0

**Promotional text (170):**

> Keep your whole Apple Health history on a server you run: heart rate, sleep, workouts and
> every other type, synced in the background and stored as files you control.

**Description (4000):**

> Health Relay copies your Apple Health data to a server you run yourself, and keeps the copy
> up to date.
>
> Your iPhone and Apple Watch record a lot about you: every heartbeat, every night of sleep,
> every run. Health Relay reads that history from the Health app, with your permission, and
> sends it to your own server: a small program, healthkit-relay, on a computer at home, a NAS,
> a Raspberry Pi or a cloud server you rent. Your data goes from your iPhone straight to your
> server, and only there.
>
> HOW IT WORKS
> • Set up the healthkit-relay server (Docker, a single binary or NixOS; about ten minutes,
>   guide at leblancfg.com/healthkit-relay).
> • Open the server's setup page and scan its QR code with the iPhone's Camera, or type the
>   server address and setup code in the app.
> • Choose what Health Relay may read in Apple Health's permission sheet.
> • The first sync sends your whole history. After that, Health Relay syncs in the background
>   several times a day; open the app to sync right away.
>
> WHAT IT COPIES
> The data types Apple Health lets apps read, the ones you allow: steps, heart rate, heart
> rate variability, resting heart rate, VO2 max, sleep stages, workouts with their routes,
> ECG recordings, blood oxygen, blood pressure, weight and body measurements, nutrition,
> mindfulness, State of Mind, cycle tracking, medications you log, hearing and more (clinical
> health records are not part of this version). Each record keeps its source, device and
> details. Deletions in Health are copied too.
>
> ON YOUR SERVER
> The server keeps everything in Parquet files, sorted by type and month, ready for a
> notebook, a script or a SQL query. It can also send the numbers to a metrics database for
> charts and dashboards, and offers a read-only query API.
>
> PRIVATE BY DESIGN
> • Health Relay reads your Health data and leaves it as it is.
> • It connects only to the server you set up: over HTTPS, or plain HTTP on your own local
>   network if you choose.
> • Analytics, advertising and tracking are absent from the app; your data stays between your
>   iPhone and your server.
> • Erase Server Data and Disconnect deletes, on your server, everything your iPhone sent.
>
> WHAT YOU NEED
> An iPhone with the Health app, and a computer or server that stays on, where you install
> healthkit-relay. Health Relay works together with that server.

**Keywords (100):**

```
export,backup,self-hosted,server,sync,homelab,archive,heart rate,sleep,steps,workouts,nas
```

(Name and subtitle words count already: no need to repeat "health", "data", "relay".
Apple's and other trademarks are left out, as guideline 2.3.7 asks.)

**What's New (first version, 4000):**

> First release on the App Store.

## Age rating answers

Every content description: **None**. Medical or Treatment Information: **None** (the app
shows sync status and record counts, never health advice). Health or Wellness Topics:
**None** (it gives no self-care or lifestyle recommendations). In-app controls: none.
Capabilities: Unrestricted Web Access **No** (links open Safari), User-Generated Content
**No**, Messaging **No**, Advertising **No**, Social media **No**. Chance-based activities:
**None**. Result: **4+**.

## App Privacy (questionnaire)

- **Do you or your third-party partners collect data from this app?** **No, we do not
  collect data from this app.** App Store Connect's definition of "collect": transmitting
  data off the device so that the developer or its partners can access it. Health Relay
  sends Health data only to a server the user runs and controls; the developer has no access
  to it, and the app has no analytics, crash reporting, advertising or third-party SDK.
  The developer's demo server for App Review stores no Health data either: it validates each
  batch, answers and drops it (counts only), so nothing is retained beyond servicing the
  request.
- Result on the product page: **Data Not Collected**.
- Tracking: **No** (`NSPrivacyTracking` false in the privacy manifest; no tracking domains).

## Export compliance

`ITSAppUsesNonExemptEncryption` is `false` in Info.plist: the app uses only HTTPS through
iOS's URL loading system, which is exempt. App Store Connect asks nothing more for each
build.

## Other App Store Connect answers

- **Sign-in required:** Yes. App Review Information › Sign-in information: User name
  `https://health-demo.tail3f3b8.ts.net` (the server address), Password: the demo setup
  code. The app has no account; the setup code is what pairs it with a server.
- **Contains, displays or accesses third-party content:** No.
- **Advertising identifier (IDFA):** No.
- **Government, regulated, medical device:** No. Health Relay is a personal data copy tool;
  it diagnoses, treats and advises nothing.
- **Encryption:** exempt (above).
- **Content rights:** all content (code, icon, text) is the developer's.
