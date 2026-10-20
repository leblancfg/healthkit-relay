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
            + "\(AppState.now.description), \(PowerState.now.description)")
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
            MainActor.assumeIsolated {
                Log.info("app: protected data available (unlocked)")
                let work = Task { await SyncEngine.shared.dataBecameAvailable(.unlock) }
                let task = BackgroundTask.begin("sync after unlock") {
                    work.cancel()
                    SyncEngine.shared.backgroundTimeOver()
                }
                Task {
                    _ = await work.value
                    task.end()
                }
            }
        }
        NotificationCenter.default.addObserver(
            forName: UIApplication.didEnterBackgroundNotification, object: nil, queue: .main
        ) { _ in
            Log.info("app: in the background")
            RefreshTask.schedule()
        }
        // When iOS wakes the app depends on these: logged when they change while the app runs,
        // so the log shows what the phone was doing between wakes.
        NotificationCenter.default.addObserver(
            forName: UIApplication.protectedDataWillBecomeUnavailableNotification, object: nil, queue: .main
        ) { _ in Log.info("app: protected data unavailable (locked)") }
        for name in [UIDevice.batteryStateDidChangeNotification, Notification.Name.NSProcessInfoPowerStateDidChange] {
            NotificationCenter.default.addObserver(forName: name, object: nil, queue: .main) { _ in
                MainActor.assumeIsolated { Log.info("app: power: \(PowerState.now.description)") }
            }
        }
        // Woken in the background (HealthKit, refresh) with the phone unlocked since a sync was
        // skipped or failed in an earlier process: make up for it now, not at the next unlock.
        if application.applicationState == .background, application.isProtectedDataAvailable {
            let work = Task {
                if await SyncEngine.shared.pending != nil { await SyncEngine.shared.dataBecameAvailable(.unlock) }
            }
            let task = BackgroundTask.begin("owed sync") {
                work.cancel()
                SyncEngine.shared.backgroundTimeOver()
            }
            Task {
                await work.value
                task.end()
            }
        }
        return true
    }
}
