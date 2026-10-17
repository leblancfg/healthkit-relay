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

## Use

1. Enter the server URL (`https://…`; the app appends `/v1/samples`) and the token. Save.
2. **Grant access** and allow the data types you want to share.
3. **Sync now**. The first sync uploads all history, which can take a while; keep the app
   open. After that iOS wakes the app in the background when new data arrives (about
   hourly, at the system's discretion; Low Power Mode and force-quitting the app pause it).

**Re-send all history** forgets the sync position; the server drops what it already has.

## Code

| File | |
|------|-|
| `HealthTypes.swift` | the data types to sync, with their units: add a type here |
| `SyncEngine.swift` | observer queries, background delivery, anchored queries, lock handling |
| `Uploader.swift` | batched POST with retry and exponential backoff |
| `WireFormat.swift` | HealthKit objects to the JSON the server expects |
| `AnchorStore.swift` | persisted query anchors |
| `Settings.swift` | server URL and token in the Keychain |
| `ContentView.swift` | the single screen |
