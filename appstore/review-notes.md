# App Review notes: Health Relay 1.0.0

Pasted into App Store Connect › App Review Information › Notes (4000 characters). The
setup code is filled in from the demo server's secret at submission time (`{{DEMO_CODE}}`
here); the filled copy lives outside the repository. Sign-in required: **Yes**, with the
server address as the user name and the setup code as the password (listing.md).

---

Health Relay copies the user's Apple Health data to a server the user runs (the
open-source healthkit-relay program on a home computer, NAS or cloud server). It has no
account; data goes only to the user's server. For this review we run a demo server.

DEMO SERVER
Address: https://health-relay-demo.leblancfg.com
Setup code: {{DEMO_CODE}}
The code pairs many devices. The demo server stores nothing about its users: it checks each
batch, answers it and drops it, counting records only (all devices together, reset hourly),
and keeps no device list.

STEPS (about 2 minutes)
1. Open Health Relay. The welcome screen explains that it works with your own server.
2. Tap "Enter Server Details Instead" at the bottom.
3. Server address: https://health-relay-demo.leblancfg.com
   Setup code: {{DEMO_CODE}}
   Tap Connect.
   (Alternative: open https://health-relay-demo.leblancfg.com/setup?code={{DEMO_CODE}} in Safari
   on the device and tap "Open in Health Relay", then Connect.)
4. The app says what to pick in Health's sheet; tap Continue. In Apple Health's permission
   sheet, turn on all categories (or the ones you like) and tap Allow.
5. The app syncs: "Up to date", the records the demo server received and the records sent
   appear. With an empty Health app the sync sends little and still completes. Pull down or
   tap Sync Now to sync again.
6. The gear (top right) opens Advanced: server and pairing, data types read, types Health
   shares item by item, the erase options (each asks first), the diagnostic log, Privacy
   Policy and Help.

PERMISSIONS AND WHY
- HealthKit read access: the app reads the Health data the user allows, to copy it to the
  user's server. It requests read access only (toShare is empty); the
  NSHealthUpdateUsageDescription string is there because the authorization API covers both.
- HealthKit background delivery: new Health data wakes the app so it can sync.
- Background modes: "fetch" (background app refresh) and "processing" (a catch-up sync while
  charging) run the same sync to the user's server.
- Local network (NSLocalNetworkUsageDescription): only when the user's server is on their
  home network, e.g. http://192.168.1.10:8080. NSAllowsLocalNetworking lets plain HTTP reach
  such servers; the app itself accepts http:// only for private addresses and .local or
  single-label names, and HTTPS for everything else. The demo server uses HTTPS, so this
  prompt does not appear during review.

DATA AND PRIVACY
- The developer collects no data: no analytics, ads, tracking or third-party SDKs. Privacy
  label: Data Not Collected.
- Pending uploads stay out of iCloud and device backups; nothing is stored in iCloud.
- The App Store version reads no clinical health records.
- No accounts: the user pairs with their own server using a setup code from it, and can at
  any time disconnect (Advanced › Disconnect) or stop syncing and erase everything the
  device sent (Advanced › Erase Server Data and Disconnect).
- Health Relay provides no medical service, advice or diagnosis, and the developer holds no
  user's data: it copies the user's own Health data to a computer the user owns, like a
  backup tool. The developer receives and keeps none of it (the demo server above drops
  what it receives).

WHY A SERVER
Health Relay is the iPhone half of a self-hosted tool: its purpose is to keep a copy of the
user's Health history on their own computer, under their control, for their own charts,
queries and backups. The server is a program the user runs on a computer, not an iOS app or
an online service. The demo server stands in for it during review.

DEMO VIDEO
The attached screen recording shows these steps on an iPhone simulator with the demo server.

Contact: healthrelay@leblancfg.com
