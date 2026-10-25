import Foundation
import Network
import UIKit

// Uploads that finish after iOS suspends the app. Each batch is written to a file and sent by a
// background URLSession: the system (nsurlsessiond) carries on with a transfer when the app is
// suspended or ended, and wakes the app when it is done. A sync still running waits for each
// upload as before, so anchors advance in order and only after the server confirmed. When a
// sync is cut short (background time over), its upload under way goes on; the anchor that
// upload confirms is saved when it finishes, in this process or a relaunch.

/// One upload's bookkeeping, saved next to its body: what to check in the answer and which
/// sync position to move once the server confirmed it.
struct PendingUpload: Codable, Equatable, Sendable {
    /// Records in the batch: a server without "records" in its answer dropped them.
    var records: Int
    var items: Int
    /// Set on the last batch of a page: the page's anchor.
    var commit: UploadCommit?
}

/// Sends one upload: the status code and the start of the answer's body.
protocol UploadTransport: Sendable {
    /// `direct`: through a standard session whatever the app's state.
    func send(_ request: URLRequest, body: Data, upload: PendingUpload, direct: Bool) async throws -> (Int, Data)
}

/// Upload bodies and their bookkeeping on disk (Application Support/uploads, out of backups),
/// readable after the first unlock, as the system reads the body while the iPhone is locked. Each file is
/// removed once its upload is over, whatever the outcome.
struct UploadStore: Sendable {
    let directory: URL

    init(name: String = "uploads") {
        directory = AppDirectory.make(name)
    }

    func bodyURL(_ id: String) -> URL { directory.appendingPathComponent(id + ".body") }
    private func infoURL(_ id: String) -> URL { directory.appendingPathComponent(id + ".json") }

    /// Writes an upload; returns its id.
    func save(_ body: Data, _ upload: PendingUpload) throws -> String {
        let id = UUID().uuidString
        let options: Data.WritingOptions = [.atomic, .completeFileProtectionUntilFirstUserAuthentication]
        try JSONEncoder().encode(upload).write(to: infoURL(id), options: options)
        try body.write(to: bodyURL(id), options: options)
        return id
    }

    func load(_ id: String) -> PendingUpload? {
        guard let data = try? Data(contentsOf: infoURL(id)) else { return nil }
        return try? JSONDecoder().decode(PendingUpload.self, from: data)
    }

    func remove(_ id: String) {
        try? FileManager.default.removeItem(at: bodyURL(id))
        try? FileManager.default.removeItem(at: infoURL(id))
    }

    /// Ids of the uploads on disk.
    func ids() -> Set<String> {
        let names = (try? FileManager.default.contentsOfDirectory(atPath: directory.path)) ?? []
        return Set(names.map { ($0 as NSString).deletingPathExtension })
    }

    /// Removes uploads the system no longer has (their transfer ended while no process could
    /// hear it), written more than `age` seconds ago; returns how many.
    @discardableResult
    func removeAll(except live: Set<String>, olderThan age: TimeInterval) -> Int {
        var removed = 0
        for id in ids().subtracting(live) {
            let written = (try? FileManager.default.attributesOfItem(atPath: infoURL(id).path)[.modificationDate] as? Date)
                ?? (try? FileManager.default.attributesOfItem(atPath: bodyURL(id).path)[.modificationDate] as? Date)
                ?? .distantPast
            guard Date.now.timeIntervalSince(written) > age else { continue }
            remove(id)
            removed += 1
        }
        return removed
    }
}

/// The background URLSession for uploads, and its delegate.
final class BackgroundUploads: NSObject, UploadTransport, URLSessionDataDelegate, @unchecked Sendable {
    static let shared = BackgroundUploads()
    static var identifier: String { (Bundle.main.bundleIdentifier ?? "org.example.healthkitrelay") + ".uploads" }
    /// The longest a running sync waits for one upload; then it cancels it and retries. A
    /// background session waits for a network (or a server that is down) rather than fail.
    static let timeout: Duration = .seconds(150)
    /// An upload that has sent nothing after this is cancelled, and uploads go through a
    /// standard session (which fails at once when the server is down) for `directFor`.
    static let connectTimeout: Duration = .seconds(20)
    static let directFor: TimeInterval = 10 * 60
    private static let maxAnswer = 64 << 10

    let store: UploadStore
    private let sessionIdentifier: String
    /// Moves a sync position for an upload whose sync is gone; returns whether it moved.
    private let commit: @Sendable (UploadCommit) async -> Bool
    private let lock = NSLock()
    private var session: URLSession?
    /// Syncs waiting for their upload, by upload id.
    private var waiting: [String: CheckedContinuation<(Int, Data), Error>] = [:]
    /// Answers so far, by task.
    private var answers: [Int: Data] = [:]
    /// Uploads finished since the last relaunch whose sync was gone: their commits, and items.
    private var orphans: [Task<Bool, Never>] = []
    private var orphanItems = 0
    private var relaunchDone: (() -> Void)?
    private var eventsDelivered = false
    private var onRelaunch: (@Sendable (_ moved: Int, _ items: Int) async -> Void)?
    private var directUntil = Date.distantPast

    init(identifier: String = BackgroundUploads.identifier, store: UploadStore = UploadStore(),
         commit: @escaping @Sendable (UploadCommit) async -> Bool = BackgroundUploads.commitInEngine) {
        sessionIdentifier = identifier
        self.store = store
        self.commit = commit
    }

    @Sendable private static func commitInEngine(_ commit: UploadCommit) async -> Bool {
        await SyncEngine.shared.commitFinishedUpload(commit)
    }

    /// Creates the session (at launch: the system then delivers what finished meanwhile) and
    /// removes files of uploads it no longer has. `onRelaunch` runs after the system woke the
    /// app for finished uploads.
    func start(onRelaunch: @escaping @Sendable (_ moved: Int, _ items: Int) async -> Void) {
        whenRelaunched(onRelaunch)
        urlSession.getAllTasks { tasks in
            let live = Set(tasks.compactMap(\.taskDescription))
            let removed = self.store.removeAll(except: live, olderThan: 120)
            if !tasks.isEmpty || removed > 0 {
                Log.info("upload: \(tasks.count) uploads under way in the system" + (removed > 0 ? ", \(removed) old ones removed" : ""))
            }
        }
    }

    func whenRelaunched(_ onRelaunch: @escaping @Sendable (_ moved: Int, _ items: Int) async -> Void) {
        lock.withLock { self.onRelaunch = onRelaunch }
    }

    private var urlSession: URLSession {
        lock.withLock {
            if let session { return session }
            let config = URLSessionConfiguration.background(withIdentifier: sessionIdentifier)
            config.isDiscretionary = false
            config.sessionSendsLaunchEvents = true
            config.timeoutIntervalForResource = 24 * 60 * 60
            let queue = OperationQueue()
            queue.maxConcurrentOperationCount = 1
            let s = URLSession(configuration: config, delegate: self, delegateQueue: queue)
            session = s
            return s
        }
    }

    /// Sends an upload. With the app on screen (and `direct` false), through the background
    /// session: a transfer started in the foreground is not discretionary, and goes on if the
    /// user leaves the app. In the background, through a standard session, which starts at once
    /// (the system runs background-session transfers started in the background at its
    /// discretion); when the background time ends during such an upload, the upload moves to
    /// the background session to finish there.
    func send(_ request: URLRequest, body: Data, upload: PendingUpload, direct: Bool) async throws -> (Int, Data) {
        guard NetworkStatus.shared.isOnline else { throw UploadError.failed("no network connection") }
        let id = try store.save(body, upload)
        let onScreen = await MainActor.run { UIApplication.shared.applicationState == .active }
        let session = onScreen && !direct && lock.withLock { Date.now >= directUntil }
        return try await (session ? inSession(id, request) : now(id, request))
    }

    /// Through a standard session; handed to the background session if cut short.
    private func now(_ id: String, _ request: URLRequest) async throws -> (Int, Data) {
        let file = store.bodyURL(id)
        let transfer = Task { try await AddressHandoff.noRedirects.upload(for: request, fromFile: file) }
        let handedOff = Flag()
        do {
            let (data, response) = try await withTaskCancellationHandler {
                try await transfer.value
            } onCancel: {
                transfer.cancel()
                handedOff.set()
                handOff(id, request)
            }
            if !handedOff.isSet { store.remove(id) }
            return ((response as? HTTPURLResponse)?.statusCode ?? 0, data.prefix(Self.maxAnswer))
        } catch {
            if handedOff.isSet { throw CancellationError() }
            store.remove(id)
            throw error
        }
    }

    /// Starts the background-session transfer of a saved upload nobody waits for.
    private func handOff(_ id: String, _ request: URLRequest) {
        let task = urlSession.uploadTask(with: request, fromFile: store.bodyURL(id))
        task.taskDescription = id
        task.resume()
        Log.info("upload: out of background time; the upload under way goes on in the system")
    }

    /// Through the background session, waiting for its answer.
    private func inSession(_ id: String, _ request: URLRequest) async throws -> (Int, Data) {
        let task = urlSession.uploadTask(with: request, fromFile: store.bodyURL(id))
        task.taskDescription = id
        let timer = Task { [weak self, weak task] in
            try? await Task.sleep(for: Self.connectTimeout)
            guard !Task.isCancelled, let self, self.isWaiting(id), let task else { return }
            if task.countOfBytesSent == 0 {
                Log.info("upload: nothing sent after \(Self.connectTimeout) through the background session; "
                    + "sending through a standard session for \(Int(Self.directFor / 60)) minutes")
                self.lock.withLock { self.directUntil = Date.now.addingTimeInterval(Self.directFor) }
                task.cancel()
                return
            }
            try? await Task.sleep(for: Self.timeout - Self.connectTimeout)
            guard !Task.isCancelled, self.isWaiting(id) else { return }
            Log.info("upload: no answer after \(Self.timeout), trying again")
            task.cancel()
        }
        defer { timer.cancel() }
        return try await withTaskCancellationHandler {
            try await withCheckedThrowingContinuation { (cont: CheckedContinuation<(Int, Data), Error>) in
                lock.withLock { waiting[id] = cont }
                if Task.isCancelled {
                    // Cut short before it started: nothing sent.
                    lock.withLock { waiting.removeValue(forKey: id) }?.resume(throwing: CancellationError())
                    store.remove(id)
                    return
                }
                task.resume()
            }
        } onCancel: {
            // The sync is cut short (background time over): the upload goes on in the system,
            // and finished(...) moves its anchor if the server confirms it.
            lock.withLock { waiting.removeValue(forKey: id) }?.resume(throwing: CancellationError())
        }
    }

    private func isWaiting(_ id: String) -> Bool { lock.withLock { waiting[id] != nil } }

    /// Same scheme, host and port.
    static func sameOrigin(_ a: URL, _ b: URL) -> Bool {
        func key(_ u: URL) -> String {
            let scheme = u.scheme?.lowercased() ?? ""
            return "\(scheme)://\(u.host()?.lowercased() ?? ""):\(u.port ?? (scheme == "https" ? 443 : 80))"
        }
        return key(a) == key(b)
    }

    func urlSession(_ session: URLSession, dataTask: URLSessionDataTask, didReceive data: Data) {
        lock.withLock {
            var answer = answers[dataTask.taskIdentifier] ?? Data()
            answer.append(data.prefix(max(0, Self.maxAnswer - answer.count)))
            answers[dataTask.taskIdentifier] = answer
        }
    }

    func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: Error?) {
        let answer = lock.withLock { answers.removeValue(forKey: task.taskIdentifier) } ?? Data()
        var code = (task.response as? HTTPURLResponse)?.statusCode ?? 0
        var error = error
        // A background session follows redirects on its own (iOS gives the app no say). An
        // answer from another address than the server's confirms nothing: the batch counts as
        // not sent, and its anchor stays.
        if let from = task.originalRequest?.url, let to = task.currentRequest?.url, !Self.sameOrigin(from, to) {
            Log.error("upload: the server redirected the upload to \(to.host() ?? "another address"); not counted as sent")
            code = 0
            error = UploadError.failed("the server redirected the upload to another address")
        }
        finished(task.taskDescription ?? "", code: code, answer: answer, error: error)
    }

    /// An upload is over. A sync waiting for it gets the answer; otherwise (its sync was cut
    /// short, or ran in an earlier process) the anchor it carries moves if the server
    /// confirmed every item.
    func finished(_ id: String, code: Int, answer: Data, error: Error?) {
        let waiter = lock.withLock { waiting.removeValue(forKey: id) }
        let upload = store.load(id)
        store.remove(id)
        if let waiter {
            if let error { waiter.resume(throwing: error) } else { waiter.resume(returning: (code, answer)) }
            return
        }
        guard let upload else { return }
        guard error == nil, Uploader.confirms(code, answer, records: upload.records) else {
            Log.info("upload: one finished after its sync without confirmation"
                + (error.map { ": \($0.localizedDescription)" } ?? " (HTTP \(code))") + "; the next sync sends it again")
            return
        }
        let moved = upload.commit.map { c in Task { await self.commit(c) } }
        lock.withLock {
            orphanItems += upload.items
            if let moved { orphans.append(moved) }
        }
    }

    /// The system woke the app for finished uploads (application(_:handleEventsForBackgroundURLSession:)).
    func relaunched(_ completion: @escaping () -> Void) {
        let delivered = lock.withLock { () -> Bool in
            relaunchDone = completion
            return eventsDelivered
        }
        if delivered { eventsFinished() }
    }

    func urlSessionDidFinishEvents(forBackgroundURLSession session: URLSession) {
        let waitingForCall = lock.withLock { () -> Bool in
            eventsDelivered = relaunchDone == nil
            return relaunchDone != nil
        }
        if waitingForCall { eventsFinished() }
    }

    /// Every finished upload was delivered: their anchors move, then the system hears the app
    /// is done with them, and `onRelaunch` (the next sync) runs.
    private func eventsFinished() {
        let (done, commits, items, next) = lock.withLock { () -> ((() -> Void)?, [Task<Bool, Never>], Int, (@Sendable (Int, Int) async -> Void)?) in
            defer {
                relaunchDone = nil
                eventsDelivered = false
                orphans = []
                orphanItems = 0
            }
            return (relaunchDone, orphans, orphanItems, onRelaunch)
        }
        Task {
            var moved = 0
            for c in commits where await c.value { moved += 1 }
            Log.info("upload: woken for finished uploads: \(items) items confirmed, \(moved) anchors moved")
            await next?(moved, items)
            DispatchQueue.main.async { done?() }
        }
    }
}

/// A flag set once, from any thread.
final class Flag: @unchecked Sendable {
    private let lock = NSLock()
    private var value = false

    func set() { lock.withLock { value = true } }
    var isSet: Bool { lock.withLock { value } }
}

/// Whether the iPhone has a network path (NWPathMonitor). The monitor reports changes only
/// while the app runs, so this fails uploads at once when offline (a background session would
/// wait for a network instead), and syncs owed after a failure when the network comes back
/// while the app runs.
final class NetworkStatus: @unchecked Sendable {
    static let shared = NetworkStatus()

    private let monitor = NWPathMonitor()
    private let lock = NSLock()
    private var satisfied: Bool?
    private var onReconnect: (@Sendable () -> Void)?

    func start(onReconnect: @escaping @Sendable () -> Void) {
        lock.withLock { self.onReconnect = onReconnect }
        monitor.pathUpdateHandler = { [weak self] path in self?.update(path.status == .satisfied) }
        monitor.start(queue: DispatchQueue(label: "network-status"))
    }

    /// Unknown (before the first answer) counts as online.
    var isOnline: Bool { lock.withLock { satisfied ?? true } }

    func update(_ online: Bool) {
        let (was, reconnect) = lock.withLock { () -> (Bool?, (@Sendable () -> Void)?) in
            defer { satisfied = online }
            return (satisfied, onReconnect)
        }
        if online, was == false { reconnect?() }
    }
}
