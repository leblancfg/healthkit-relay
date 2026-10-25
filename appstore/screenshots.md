# Screenshots and demo video

## Screenshots (6.9-inch iPhone, 1320 x 2868)

Made by the UI test `testManualSetupWithTheDemoServer`, which does what App Review does:
a fresh simulator, the demo server's address and setup code typed in, Health access
allowed, the first sync. Run the **App Store screenshots** workflow
(`.github/workflows/appstore-screenshots.yml`, repository secrets `HKR_DEMO_SERVER` and
`HKR_DEMO_CODE`) and download its `appstore-screenshots` artifact. The status bar shows
9:41, full battery and signal. The simulator's Health app is empty, so the screenshots hold
no personal data; the records count is the demo server's.

| # | File | Shows | Caption idea |
|---|---|---|---|
| 1 | `01-welcome` | first run: what the app does, "Works with your own server" | Your Health data, on your own server |
| 2 | `02-server-details` | server address and setup code typed in | Pair in seconds with a setup code |
| 3 | `03-health-step` | what to pick in Health's sheet | You choose what it reads |
| 4 | `04-home` | "Up to date", records on the server, sync in the background | Syncs in the background several times a day |
| 5 | `05-advanced` | server, data types, erase, privacy policy | Everything in one place, nothing hidden |
| 6 | `06-data-types` | the data types read | Every type Apple Health lets apps read |

App Store Connect scales the 6.9-inch set for smaller iPhones. The app runs on iPhone only
(`TARGETED_DEVICE_FAMILY` 1), so it needs no iPad screenshots. Upload them as they are
(screenshots of the app in use, guideline 2.3.3); captions are optional.

## Demo video for App Review (1 to 2 minutes, not for the product page)

The screenshot workflow records its run as `review-demo.mp4` (in the same artifact): the
welcome screen, the demo address and code typed in, Health's sheet, the sync, Advanced. It
shows the real demo code, so it goes only to App Review (App Review Information ›
Attachment), never to the product page. To record by hand instead, on a real iPhone
(Control Center › Screen Recording) or a simulator (`xcrun simctl io <udid> recordVideo
review.mp4`), follow:

1. Launch on a fresh install: the welcome screen (3 s).
2. Enter Server Details Instead; type the demo address and code; Connect.
3. Continue; Apple Health's permission sheet; turn on all; Allow.
4. Home: Syncing, then Up to date with the records count; pull to refresh.
5. Advanced: the server, data types read, Erase Server Data and Start Over (show the
   confirmation, cancel), Privacy Policy opening in Safari.
6. Optional, 10 s: the demo server's setup page in Safari showing the same server, to show
   where the data went.
