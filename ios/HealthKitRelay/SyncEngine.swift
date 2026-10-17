import Foundation
import HealthKit
import UIKit

enum SyncError: LocalizedError {
    case locked

    var errorDescription: String? {
        switch self {
        case .locked: "Phone is locked; will sync after the next unlock"
        }
    }
}

/// What the UI shows. Updated by the engine on the main actor.
@MainActor
final class SyncStatus: ObservableObject {
    static let shared = SyncStatus()

    @Published var running = false
    @Published var detail = ""
    @Published var lastError: String?
    @Published var lastSuccess: Date? {
        didSet { UserDefaults.standard.set(lastSuccess, forKey: "lastSuccess") }
    }
    @Published var lastUploaded = 0 {
        didSet { UserDefaults.standard.set(lastUploaded, forKey: "lastUploaded") }
    }

    private init() {
        lastSuccess = UserDefaults.standard.object(forKey: "lastSuccess") as? Date
        lastUploaded = UserDefaults.standard.integer(forKey: "lastUploaded")
    }
}

/// Incremental sync: one HKAnchoredObjectQuery per type, resumed from a persisted anchor, so
/// each run uploads only new samples and deletions. An anchor advances only after the
/// server stored the batch, so nothing is lost on failure; the server drops duplicates.
actor SyncEngine {
    static let shared = SyncEngine()

    let store = HKHealthStore()
    private let anchors = AnchorStore()
    private let batchSize = 2000
    private var inFlight: [String: Task<Int, Error>] = [:]
    private var observing = false
    /// A sync was skipped because the phone was locked: run it after the next unlock.
    private(set) var pendingAfterUnlock = false

    static var isAvailable: Bool { HKHealthStore.isHealthDataAvailable() }

    // MARK: Authorization

    func requestAuthorization() async throws {
        try await store.requestAuthorization(toShare: [], read: HealthTypes.readTypes)
        await startObserving()
    }

    /// Whether the authorization sheet still needs to be shown. HealthKit never reveals
    /// which read permissions were granted, only whether the user has been asked.
    func needsAuthorization() async -> Bool {
        let status = try? await store.statusForAuthorizationRequest(toShare: [], read: HealthTypes.readTypes)
        return status != .unnecessary
    }

    // MARK: Background delivery

    /// Registers an observer query per type and asks HealthKit to wake the app (at most
    /// hourly) when new data arrives. Must run at every launch, including background ones.
    func startObserving() async {
        guard Self.isAvailable, !observing else { return }
        observing = true
        for synced in HealthTypes.all {
            let query = HKObserverQuery(sampleType: synced.type, predicate: nil) { _, completion, error in
                guard error == nil else { completion(); return }
                Task {
                    let task = await BackgroundTask.begin("sync \(synced.key)")
                    _ = try? await SyncEngine.shared.sync(synced)
                    await task.end()
                    completion()
                }
            }
            store.execute(query)
            do {
                try await store.enableBackgroundDelivery(for: synced.type, frequency: .hourly)
            } catch {
                // Not authorized yet: requestAuthorization() calls this again.
                observing = false
            }
        }
    }

    // MARK: Sync

    /// Syncs every type. Returns the number of records uploaded.
    @discardableResult
    func syncAll() async -> Int {
        await MainActor.run {
            SyncStatus.shared.running = true
            SyncStatus.shared.lastError = nil
        }
        var total = 0
        var firstError: Error?
        for synced in HealthTypes.all {
            do {
                total += try await sync(synced)
            } catch {
                firstError = firstError ?? error
                // Locked phone or missing settings affect every type: stop here.
                if error is SyncError || error is CancellationError { break }
                if case UploadError.notConfigured = error { break }
            }
        }
        let uploaded = total
        let failure = firstError
        await MainActor.run {
            let status = SyncStatus.shared
            status.running = false
            status.detail = ""
            if let failure {
                status.lastError = failure.localizedDescription
            } else {
                status.lastSuccess = .now
                status.lastUploaded = uploaded
            }
        }
        return total
    }

    /// Syncs one type; joins a sync of that type already in progress.
    func sync(_ synced: SyncedType) async throws -> Int {
        if let running = inFlight[synced.key] {
            return try await running.value
        }
        let task = Task { try await self.run(synced) }
        inFlight[synced.key] = task
        defer { inFlight[synced.key] = nil }
        return try await task.value
    }

    func syncAfterUnlockIfPending() async {
        guard pendingAfterUnlock else { return }
        pendingAfterUnlock = false
        await syncAll()
    }

    /// Forgets all anchors, so the next sync re-sends all history.
    func resetAnchors() {
        anchors.reset()
    }

    private func run(_ synced: SyncedType) async throws -> Int {
        let settings = ServerSettings.load()
        guard settings.isComplete else { throw UploadError.notConfigured }
        guard await MainActor.run(body: { UIApplication.shared.isProtectedDataAvailable }) else {
            pendingAfterUnlock = true
            throw SyncError.locked
        }
        let uploader = Uploader(settings: settings)
        let device = await Self.deviceInfo()
        var total = 0

        while true {
            try Task.checkCancellation()
            let descriptor = HKAnchoredObjectQueryDescriptor(
                predicates: [.sample(type: synced.type)],
                anchor: anchors.anchor(for: synced.key),
                limit: batchSize)
            let result: HKAnchoredObjectQueryDescriptor<HKSample>.Result
            do {
                result = try await descriptor.result(for: store)
            } catch let error as HKError where error.code == .errorDatabaseInaccessible {
                pendingAfterUnlock = true
                throw SyncError.locked
            }

            if result.addedSamples.isEmpty && result.deletedObjects.isEmpty {
                try anchors.save(result.newAnchor, for: synced.key)
                return total
            }
            let batch = WireBatch(
                device: device,
                samples: result.addedSamples.map { WireEncoder.sample($0, as: synced) },
                deletions: result.deletedObjects.map { WireEncoder.deletion($0, type: synced.type) })
            try await uploader.upload(batch)
            try anchors.save(result.newAnchor, for: synced.key)

            total += batch.samples.count + batch.deletions.count
            let shown = total
            let name = synced.type.identifier
                .replacingOccurrences(of: "HKQuantityTypeIdentifier", with: "")
                .replacingOccurrences(of: "HKCategoryTypeIdentifier", with: "")
            await MainActor.run { SyncStatus.shared.detail = "\(name): \(shown) uploaded" }
        }
    }

    @MainActor
    private static func deviceInfo() -> WireDevice {
        var info = utsname()
        uname(&info)
        let model = withUnsafeBytes(of: &info.machine) { buf in
            String(decoding: buf.prefix(while: { $0 != 0 }), as: UTF8.self)
        }
        return WireDevice(
            model: model,
            systemVersion: UIDevice.current.systemVersion,
            appVersion: Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "")
    }
}

/// Asks iOS for extra time to finish an upload when the app is in the background.
@MainActor
final class BackgroundTask {
    private var id: UIBackgroundTaskIdentifier = .invalid

    static func begin(_ name: String) -> BackgroundTask {
        let task = BackgroundTask()
        task.id = UIApplication.shared.beginBackgroundTask(withName: name) { [weak task] in
            task?.end()
        }
        return task
    }

    func end() {
        guard id != .invalid else { return }
        UIApplication.shared.endBackgroundTask(id)
        id = .invalid
    }
}
