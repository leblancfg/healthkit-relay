# App Review notes: Health Relay 1.0.0

Pasted into App Store Connect › App Review Information › Notes (4000 characters). The
setup code is filled in from the demo server's secret at submission time (`{{DEMO_CODE}}`
here); the filled copy lives outside the repository. Sign-in required: **No**.

---

Health Relay copies the user's Apple Health data to a server the user runs themselves (the
open-source healthkit-relay program on a home computer, NAS or cloud server). It has no
account and no developer-run backend: the data goes only to the user's own server. For
this review we run a demo server you can reach from the internet.

DEMO SERVER
Address: https://health-demo.tail3f3b8.ts.net
Setup code: {{DEMO_CODE}}
The code pairs many devices. The demo server keeps no Health data: it checks each batch,
answers it and drops it, counting records only (the count resets every hour).

STEPS (about 2 minutes)
1. Open Health Relay. The welcome screen explains that it works with your own server.
2. Tap "Enter Server Details Instead" at the bottom.
3. Server address: https://health-demo.tail3f3b8.ts.net
   Setup code: {{DEMO_CODE}}
   Tap Connect.
   (Alternative: open https://health-demo.tail3f3b8.ts.net/setup?code={{DEMO_CODE}} in Safari
   on the device and tap "Open in Health Relay", then Connect.)
4. The app says what to pick in Health's sheet; tap Continue. In Apple Health's permission
   sheet, turn on all categories (or the ones you like) and tap Allow.
5. The app syncs: "Up to date", the records the demo server received and the records sent
   appear. With an empty Health app the sync sends little and still completes. Pull down or
   tap Sync Now to sync again.
6. The gear (top right) opens Advanced: the server and pairing, the data types read, the
   types Health shares item by item, Erase Server Data and Start Over (asks first, then has
   the server delete everything this device sent), the diagnostic log, Privacy Policy and Help.

PERMISSIONS AND WHY
- HealthKit read access (NSHealthShareUsageDescription): the app reads the Health data the
  user allows, to copy it to the user's server. It requests read access only and writes
  nothing to Health. NSHealthUpdateUsageDescription is present because HealthKit's
  authorization API covers both directions; the app never requests write access.
- HealthKit background delivery: new Health data wakes the app so it can sync.
- Background modes: "fetch" (background app refresh, a fallback when HealthKit does not wake
  the app) and "processing" (a catch-up sync while the iPhone charges). Both run the same
  sync to the user's server.
- Local network (NSLocalNetworkUsageDescription): only when the user's server is on their
  home network, e.g. http://192.168.1.10:8080. App Transport Security allows plain HTTP to
  local addresses only (NSAllowsLocalNetworking); every other address uses HTTPS. The demo
  server uses HTTPS, so this prompt does not appear during review.

DATA AND PRIVACY
- The developer collects no data: Health data goes only to the user's server. No analytics,
  ads, tracking or third-party SDKs. Privacy label: Data Not Collected.
- Pending uploads stay out of iCloud and device backups; nothing is stored in iCloud.
- The App Store version reads no clinical health records.
- No accounts: the user pairs with their own server using a setup code from it, and can
  disconnect (Advanced › Disconnect) or erase their data on the server (Advanced › Erase
  Server Data and Start Over) at any time.
- Health Relay provides no medical service, advice or diagnosis: it copies the user's own
  data to the user's own computer, like a backup tool.

DEMO VIDEO
A screen recording of these steps on an iPhone is attached [link added at submission].

Contact: healthrelay@leblancfg.com
