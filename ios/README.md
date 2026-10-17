# Health Relay (iOS)

SwiftUI app, iOS 17+. Reads HealthKit, uploads to a healthkit-relay server.

## Build

Requirements: Xcode 16 or newer, [XcodeGen](https://github.com/yonaskolb/XcodeGen), and a
paid Apple Developer Program membership (HealthKit and background delivery need it; a free
personal team cannot sign HealthKit apps).

1. `cp Config/Local.xcconfig.example Config/Local.xcconfig` and set:
   - `HKR_TEAM_ID`: your Team ID (developer.apple.com › Membership).
   - `HKR_BUNDLE_ID`: a bundle identifier of your own, e.g. `com.yourname.healthkitrelay`.
2. `xcodegen generate` (or `make ios-project` from the repository root).
3. Either open `HealthKitRelay.xcodeproj` and run on your iPhone, or from the command line:

   ```sh
   xcodebuild -project HealthKitRelay.xcodeproj -scheme HealthKitRelay -configuration Release \
     -destination generic/platform=iOS -derivedDataPath build -allowProvisioningUpdates build
   xcrun devicectl device install app --device <device id> \
     build/Build/Products/Release-iphoneos/HealthKitRelay.app
   ```

   Automatic signing registers the App ID with the HealthKit capability and creates the
   provisioning profile; Xcode must be signed in to your developer account
   (Settings › Accounts). The iPhone needs Developer Mode on (Settings › Privacy &
   Security) and must trust the Mac.

`Info.plist`, the entitlements file and the Xcode project are generated from
`project.yml`; edit that, not the generated files.

### Data types come from the SDK

Every build runs `Scripts/gen-health-types` (Python 3, standard library; Xcode ships it), which
parses `HealthKit.framework/Headers` of the active SDK and rewrites
`HealthKitRelay/Generated/SDKHealthTypes.swift`: each `HK…TypeIdentifier` constant with the
iOS version it appeared in, Apple's documented unit (quantities) or value enum (categories),
the class methods that build types without identifiers (workouts, routes, ECGs...), and the
case names of every HealthKit enum. At run time each type is built from its identifier
string through the Objective-C runtime, so a type the running iOS lacks is skipped rather
than crashing. To see what a new Xcode adds: build, then `git diff` the generated file.

The app asks for everything in one sheet. HealthKit's own rules shape it: types
needing a companion are requested together (routes with workouts, heartbeat series with HRV),
and correlations and medication dose events are read through their parts. Types granted
object by object (vision prescriptions, medications, CDA documents) and Health Records are
not part of it: each is one tap under **Advanced**, since their sheets can be slow, lead to
Apple's provider setup, or never come when Health has nothing to list, and an app cannot
see beforehand whether there is anything to pick. Health
records need the Clinical Health Records capability on your App ID (Automatic signing adds
it).

HealthKit raises Objective-C exceptions, not errors, for some types and queries (an observer
query for vision prescriptions, for one). Every query is created and run inside an
exception guard (`HealthKitSafe.swift`), so such a type is skipped and logged, never fatal.

## Use

1. **Set up**: open your server's setup page (`https://<server>/setup`) on a computer and
   scan its code with the iPhone's Camera, then tap **Open in Health Relay** (on the iPhone
   itself, tap the page's button). Or run `healthkit-relay pair` on the server and scan the
   code it prints. **Enter Server Details Instead** takes an address and the server token,
   which the app swaps for a token of its own.
2. Health asks once what Health Relay may read: select everything, then confirm. The app
   asks again by itself only when an update brings data types the sheet never showed.
3. That is all. The first sync uploads all history, which can take a while; keep the app
   open. After that iOS wakes the app in the background when new data arrives (about
   hourly, at the system's discretion; Low Power Mode and force-quitting the app pause it),
   and it also syncs whenever it is opened. **Sync Now** (or pulling down) syncs at once.

The main screen shows only how syncing goes. **Advanced** (the gear) has the server and
this iPhone's pairing, which data types are read, the types shared item by item, re-sending
all history and a diagnostic log (steps and counts, no health values).

## Code

| File | |
|------|-|
| `Scripts/gen-health-types` | build step: every HealthKit type, unit and enum name from the SDK headers |
| `Generated/SDKHealthTypes.swift` | its output (regenerated on each build, committed so the project builds without it) |
| `HealthTypes.swift` | resolves the generated types on the running iOS, skipping unknown ones |
| `SampleEncoder.swift` | every HealthKit kind to wire JSON, with series and files in chunks |
| `Units.swift` | one pinned unit per quantity type; quantities to `{value, unit}` |
| `JSON.swift` | JSON values, typed metadata, NSKeyedArchiver archives |
| `Support/HKRObjC.m` | catches the Objective-C exceptions HealthKit raises for disallowed types |
| `HealthKitSafe.swift` | every query built and run inside that guard; streams, one-shot results, timeouts |
| `SyncEngine.swift` | authorization plan, main and per-object requests, observer queries, background delivery, anchored queries, activity summaries, characteristics, medications, lock handling |
| `Uploader.swift` | batched POST with retry and exponential backoff |
| `WireFormat.swift` | batches (wire format v2), source and device fields |
| `AnchorStore.swift` | persisted query anchors |
| `Settings.swift` | server URL and token in the Keychain |
| `AppModel.swift` | setup from a link, the Health request, syncing on open |
| `Pairing.swift` | setup links, pairing and status calls |
| `ContentView.swift` | the main screen (status), first-run welcome, manual setup |
| `AdvancedView.swift` | server, data types, item-by-item types, maintenance |
| `HealthKitRelayUITests/` | the setup flow end to end on a simulator (`Scripts/ui-test`) |
