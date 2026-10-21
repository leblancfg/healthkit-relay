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
        ScheduledTask.registerAll()
        ScheduledTask.scheduleAll()
        // Uploads left under way by an earlier process report to this one.
        BackgroundUploads.shared.start { moved, items in
            await MainActor.run {
                let work = Task { await SyncEngine.shared.uploadsFinished(moved: moved, items: items) }
                let task = BackgroundTask.begin("sync after uploads") {
                    work.cancel()
                    SyncEngine.shared.backgroundTimeOver()
                }
                Task {
                    await work.value
                    task.end()
                }
            }
        }
        // Only while the app runs: a network change never wakes it.
        NetworkStatus.shared.start {
            DispatchQueue.main.async {
                MainActor.assumeIsolated { Self.networkBack() }
            }
        }
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
            MainActor.assumeIsolated {
                Log.info("app: in the background")
                ScheduledTask.scheduleAll()
                Self.leftApp()
            }
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

    /// The system woke (or kept) the app for the upload session: its uploads finished.
    func application(_ application: UIApplication, handleEventsForBackgroundURLSession identifier: String,
                     completionHandler: @escaping () -> Void) {
        guard identifier == BackgroundUploads.identifier else {
            completionHandler()
            return
        }
        Log.info("app: woken for finished uploads, \(AppState.now.description), \(PowerState.now.description)")
        BackgroundUploads.shared.relaunched(completionHandler)
    }

    /// The user left the app: one more sync in the time iOS gives now (about 30 seconds), so
    /// what came in while it was open is sent; an upload under way when that time ends goes on
    /// in the system (BackgroundUploads).
    @MainActor
    private static func leftApp() {
        let app = UIApplication.shared
        guard AppModel.shared.isConfigured, !AppModel.shared.needsHealthAccess, app.isProtectedDataAvailable else { return }
        let work = Task { await SyncEngine.shared.syncAll(trigger: .backgrounded, allAux: false) }
        let task = BackgroundTask.begin("sync on leaving") {
            work.cancel()
            SyncEngine.shared.backgroundTimeOver()
        }
        Task {
            await work.value
            task.end()
        }
    }

    /// The network came back while the app runs: a sync owed after a failure runs now.
    @MainActor
    private static func networkBack() {
        guard AppModel.shared.isConfigured, UIApplication.shared.isProtectedDataAvailable else { return }
        let work = Task {
            guard await SyncEngine.shared.pending == .failed else { return }
            Log.info("app: network back; syncing what failed")
            await SyncEngine.shared.dataBecameAvailable(.retry)
        }
        let task = BackgroundTask.begin("sync after network change") {
            work.cancel()
            SyncEngine.shared.backgroundTimeOver()
        }
        Task {
            await work.value
            task.end()
        }
    }
}
