import SwiftUI
import UIKit

@main
struct HealthKitRelayApp: App {
    @UIApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate

    var body: some Scene {
        WindowGroup {
            ContentView()
        }
    }
}

final class AppDelegate: NSObject, UIApplicationDelegate {
    func application(_ application: UIApplication,
                     didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]? = nil) -> Bool {
        // HealthKit background delivery relaunches the app in the background: the observer
        // queries must be registered on every launch.
        Task { await SyncEngine.shared.startObserving() }
        NotificationCenter.default.addObserver(
            forName: UIApplication.protectedDataDidBecomeAvailableNotification, object: nil, queue: .main
        ) { _ in
            Task { await SyncEngine.shared.syncAfterUnlockIfPending() }
        }
        return true
    }
}
