import SwiftUI
import UIKit

@main
struct HealthKitRelayApp: App {
    @UIApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate

    var body: some Scene {
        WindowGroup {
            ContentView()
                .onOpenURL { url in Task { await AppModel.shared.open(url) } }
        }
    }
}

final class AppDelegate: NSObject, UIApplicationDelegate {
    func application(_ application: UIApplication,
                     didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]? = nil) -> Bool {
        #if DEBUG
        if ProcessInfo.processInfo.arguments.contains("-hkr-reset-state") { AppModel.resetForTesting() }
        #endif
        let info = Bundle.main.infoDictionary
        Log.info("app: \(info?["CFBundleShortVersionString"] as? String ?? "?") (\(info?["CFBundleVersion"] as? String ?? "?")) launched, "
            + "\(AppState.now.description)")
        // HealthKit background delivery relaunches the app in the background and wants the
        // observer queries set up before launch ends: started here, synchronously, from the
        // types saved when they were asked for. HealthKit's answers are added once it can be
        // read (the phone may be locked now).
        SyncEngine.shared.observeAtLaunch()
        RefreshTask.register()
        RefreshTask.schedule()
        Task { await SyncEngine.shared.startObserving() }
        NotificationCenter.default.addObserver(
            forName: UIApplication.protectedDataDidBecomeAvailableNotification, object: nil, queue: .main
        ) { _ in
            Log.info("app: protected data available (unlocked)")
            Task {
                let task = await BackgroundTask.begin("sync after unlock")
                await SyncEngine.shared.dataBecameAvailable(.unlock)
                await task.end()
            }
        }
        NotificationCenter.default.addObserver(
            forName: UIApplication.didEnterBackgroundNotification, object: nil, queue: .main
        ) { _ in RefreshTask.schedule() }
        // Woken in the background (HealthKit, refresh) with the phone unlocked since a sync was
        // skipped or failed in an earlier process: make up for it now, not at the next unlock.
        if application.applicationState == .background, application.isProtectedDataAvailable {
            Task {
                let task = await BackgroundTask.begin("owed sync")
                if await SyncEngine.shared.pending != nil { await SyncEngine.shared.dataBecameAvailable(.unlock) }
                await task.end()
            }
        }
        return true
    }
}
