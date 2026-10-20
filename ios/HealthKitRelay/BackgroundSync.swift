import BackgroundTasks
import Foundation
import HealthKit
import UIKit

// Syncing while the app is closed. Three ways in: HealthKit background delivery (an observer
// query per type wakes the app when new data is saved), a background app refresh about
// hourly, and the first unlock after a sync was skipped because the phone was locked.

/// What started a sync. Every batch says it ("trigger"), so the server can tell background
/// syncs from the ones an open app ran.
enum SyncTrigger: String, Sendable {
    /// The app on screen: opening it, a button.
    case foreground
    /// HealthKit background delivery: new data of one type.
    case observer
    /// The hourly background app refresh.
    case refresh
    /// The phone was unlocked after a sync was skipped because it was locked.
    case unlock
    /// A sync again after one failed (server unreachable, out of background time).
    case retry

    /// The trigger of the sync the current task belongs to; tasks a sync starts inherit it.
    @TaskLocal static var current: SyncTrigger = .foreground
}

/// A full sync still owed: skipped because the phone was locked, or failed for a reason that
/// may pass (server unreachable, background time over). Kept in UserDefaults, so a process iOS
/// ends in between does not forget it.
enum PendingSync: String, Sendable {
    case locked, failed

    static let key = "pendingSync"

    static func load(_ defaults: UserDefaults = .standard) -> PendingSync? {
        defaults.string(forKey: key).flatMap(PendingSync.init(rawValue:))
    }

    static func save(_ value: PendingSync?, _ defaults: UserDefaults = .standard) {
        if let value { defaults.set(value.rawValue, forKey: key) } else { defaults.removeObject(forKey: key) }
    }

    /// A lock keeps its place over a failure: nothing can sync before the unlock anyway.
    static func merge(_ old: PendingSync?, _ new: PendingSync) -> PendingSync {
        old == .locked ? .locked : new
    }

    /// The trigger of the sync that makes up for it, when `fallback` started it.
    func trigger(_ fallback: SyncTrigger) -> SyncTrigger {
        self == .failed ? .retry : fallback
    }
}

/// The sample types this app has asked HealthKit for, saved when it asks, so the observer
/// queries can be set up at launch without asking HealthKit anything first (its status calls
/// can fail while the phone is locked, which is exactly when background launches happen).
enum ObservedTypes {
    static let key = "observedTypes"

    static func load(_ defaults: UserDefaults = .standard) -> Set<String>? {
        defaults.stringArray(forKey: key).map(Set.init)
    }

    /// Adds to what was saved: a type asked about stays asked about.
    static func add(_ identifiers: some Sequence<String>, _ defaults: UserDefaults = .standard) {
        let old = load(defaults)
        let new = (old ?? []).union(identifiers)
        // Never an empty list: that would observe nothing, where unknown observes everything.
        if !new.isEmpty, new != old { defaults.set(new.sorted(), forKey: key) }
    }

    /// The types to observe at launch: those asked about, or, when that is not known (an
    /// install from before 0.6.1, or settings unreadable before the first unlock), every sample
    /// type; HealthKit never wakes the app for a type it cannot read.
    static func atLaunch(_ defaults: UserDefaults = .standard, all: [SyncedType] = HealthTypes.all) -> [SyncedType] {
        let saved = load(defaults)
        return all.filter { $0.sampleType != nil && (saved?.contains($0.key) ?? true) }
    }
}

/// The observer query and background delivery of each sample type. Not on the SyncEngine
/// actor: HealthKit wants the queries set up while the app launches, synchronously.
final class Observers: @unchecked Sendable {
    private let lock = NSLock()
    private var observed = Set<String>()
    /// Types HealthKit refuses observer queries for (it raises already when creating some).
    private var unobservable = Set<String>()
    /// Background delivery enabled, or asked for and not answered yet.
    private var delivery = Set<String>()
    /// Observer calls not answered yet, see answerAll.
    private var waiting: [CompletionOnce] = []

    /// Types HealthKit may wake the app for as soon as they change, not at most hourly: data
    /// that arrives all day (from a watch too), so the app runs soon after an unlock. Each wake
    /// syncs every type. HealthKit keeps some (steps) to hourly anyway.
    static let immediate: Set<String> = [
        HKQuantityTypeIdentifier.heartRate.rawValue,
        HKQuantityTypeIdentifier.stepCount.rawValue,
        HKQuantityTypeIdentifier.activeEnergyBurned.rawValue,
        HKQuantityTypeIdentifier.distanceWalkingRunning.rawValue,
        HKCategoryTypeIdentifier.sleepAnalysis.rawValue,
    ]

    static func frequency(_ key: String) -> HKUpdateFrequency { immediate.contains(key) ? .immediate : .hourly }

    /// Keeps an observer call until it is answered or answerAll answers it.
    func wait(_ done: CompletionOnce) {
        lock.lock()
        waiting.removeAll { $0.isDone }
        waiting.append(done)
        lock.unlock()
    }

    /// Answers every observer call still open; returns how many were. For when iOS is about
    /// to suspend the app: HealthKit counts an unanswered call as a failed delivery, and stops
    /// waking an app that fails three times.
    @discardableResult
    func answerAll() -> Int {
        lock.lock()
        let open = waiting.filter { !$0.isDone }
        waiting.removeAll()
        lock.unlock()
        open.forEach { $0.call() }
        return open.count
    }

    var counts: (observed: Int, delivery: Int) {
        lock.lock()
        defer { lock.unlock() }
        return (observed.count, delivery.count)
    }

    /// Starts an observer query for each type not observed yet, then asks HealthKit to wake
    /// the app for new data of each (at most hourly, or at once for `immediate`). Returns at once: background delivery
    /// answers later, and only failures are logged.
    func observe(_ types: [SyncedType], store: HKHealthStore) {
        var refused: [String] = []
        var deliver: [(String, HKSampleType)] = []
        for synced in types {
            guard let type = synced.sampleType else { continue }
            let key = synced.key
            // Marked observed before the query starts: a query ending at once (an error) must
            // find itself there to forget, and a concurrent caller must not start a second one.
            lock.lock()
            let start = !unobservable.contains(key) && observed.insert(key).inserted
            let enable = delivery.insert(key).inserted
            lock.unlock()
            if start, store.safeExecute({ self.query(type, key, store: store) }) != nil {
                lock.lock()
                observed.remove(key)
                unobservable.insert(key)
                lock.unlock()
                refused.append(SyncEngine.shortName(key))
            }
            if enable { deliver.append((key, type)) }
        }
        if !refused.isEmpty {
            Log.info("observe: HealthKit refuses observers for \(refused.joined(separator: ", ")); they sync with the others")
        }
        enableDelivery(deliver, store: store)
    }

    private func query(_ type: HKSampleType, _ key: String, store: HKHealthStore) -> HKObserverQuery {
        HKObserverQuery(sampleType: type, predicate: nil) { query, completion, error in
            let done = CompletionOnce(completion)
            self.wait(done)
            if let error {
                // The query is over: stopped and forgotten, so the next startObserving (the
                // app opening, an unlock) starts it again. Not determined is expected for each
                // type never asked about while the asked types are unknown: not logged.
                store.stop(query)
                self.lock.lock()
                self.observed.remove(key)
                self.lock.unlock()
                if (error as? HKError)?.code != .errorAuthorizationNotDetermined {
                    Log.info("observe: \(SyncEngine.shortName(key)) woke with an error: \(error.localizedDescription)")
                }
                done.call()
                return
            }
            Task {
                let task = await BackgroundTask.begin("observer wake") { SyncEngine.shared.backgroundTimeOver() }
                await SyncEngine.shared.observerFired(key, done: done)
                await task.end()
            }
        }
    }

    private func enableDelivery(_ types: [(String, HKSampleType)], store: HKHealthStore) {
        guard !types.isEmpty else { return }
        let group = DispatchGroup()
        let failed = Failures()
        for (key, type) in types {
            group.enter()
            let once = CompletionOnce { group.leave() }
            let fail = { @Sendable (reason: String?) in
                failed.add(SyncEngine.shortName(key) + (reason.map { " (\($0))" } ?? ""))
                self.lock.lock()
                self.delivery.remove(key)  // asked again at the next startObserving
                self.lock.unlock()
            }
            let raised = HKRTry {
                store.enableBackgroundDelivery(for: type, frequency: Self.frequency(key)) { ok, error in
                    if !ok { fail(error?.localizedDescription) }
                    once.call()
                }
            }
            if let raised {
                fail(raised)
                once.call()
            }
        }
        group.notify(queue: .global()) {
            let names = failed.all
            if names.isEmpty {
                Log.info("observe: background delivery on for \(types.count) more types")
            } else {
                Log.info("observe: background delivery on for \(types.count - names.count) more types, failed for \(names.count): "
                    + names.prefix(20).joined(separator: ", ") + (names.count > 20 ? "…" : ""))
            }
        }
    }
}

/// Names collected from concurrent callbacks.
private final class Failures: @unchecked Sendable {
    private let lock = NSLock()
    private var names: [String] = []

    func add(_ name: String) {
        lock.lock()
        names.append(name)
        lock.unlock()
    }

    var all: [String] {
        lock.lock()
        defer { lock.unlock() }
        return names
    }
}

/// The background app refresh: about hourly when iOS allows it, a full incremental sync, a
/// safety net for when HealthKit does not wake the app.
enum RefreshTask {
    static var identifier: String { (Bundle.main.bundleIdentifier ?? "org.example.healthkitrelay") + ".refresh" }
    static let interval: TimeInterval = 60 * 60

    /// Must run before the app finishes launching.
    static func register() {
        let ok = BGTaskScheduler.shared.register(forTaskWithIdentifier: identifier, using: nil) { task in
            guard let task = task as? BGAppRefreshTask else {
                task.setTaskCompleted(success: false)
                return
            }
            run(task)
        }
        if !ok { Log.error("refresh: \(identifier) not registered: missing from BGTaskSchedulerPermittedIdentifiers?") }
    }

    /// Asks for a run in about an hour, unless one is asked for already (`replacing` asks anew:
    /// a launch must not keep pushing the next run back).
    static func schedule(replacing: Bool = false) {
        BGTaskScheduler.shared.getPendingTaskRequests { requests in
            if !replacing, requests.contains(where: { $0.identifier == identifier }) { return }
            let request = BGAppRefreshTaskRequest(identifier: identifier)
            request.earliestBeginDate = Date(timeIntervalSinceNow: interval)
            do {
                try BGTaskScheduler.shared.submit(request)
            } catch {
                Log.info("refresh: could not schedule: \(error.localizedDescription)")
            }
        }
    }

    private static func run(_ task: BGAppRefreshTask) {
        schedule(replacing: true)
        let finish = RefreshCompletion(task)
        let work = Task { await SyncEngine.shared.backgroundRefresh() }
        task.expirationHandler = {
            Log.info("refresh: out of background time; the rest syncs next time")
            work.cancel()
            SyncEngine.shared.backgroundTimeOver()
            finish.complete(false)
        }
        Task { finish.complete(await work.value) }
    }
}

/// Completes a background task exactly once: when its work ends or when iOS expires it.
private final class RefreshCompletion: @unchecked Sendable {
    private let lock = NSLock()
    private var task: BGTask?

    init(_ task: BGTask) { self.task = task }

    func complete(_ success: Bool) {
        lock.lock()
        let t = task
        task = nil
        lock.unlock()
        t?.setTaskCompleted(success: success)
    }
}

/// The app's state and whether protected data (HealthKit's database) is readable, for logs and
/// batches.
struct AppState: Sendable {
    var state: String
    var protectedData: Bool

    @MainActor static var now: AppState {
        let app = UIApplication.shared
        return AppState(state: name(app.applicationState), protectedData: app.isProtectedDataAvailable)
    }

    static func name(_ state: UIApplication.State) -> String {
        switch state {
        case .active: "active"
        case .inactive: "inactive"
        case .background: "background"
        @unknown default: "unknown"
        }
    }

    var description: String { "app \(state), protected data \(protectedData ? "available" : "unavailable")" }
}

/// One sync at a time for every trigger. Opening the app sets off an unlock, the foreground
/// sync and HealthKit wakes for a few hundred types within a second or two; each used to run a
/// sync of its own (one spinner, one heartbeat each). Now they all join one session:
///
/// - A session started by a HealthKit wake waits `debounce` first, so the rest of the burst
///   joins it; any other trigger starts it at once.
/// - A trigger arriving while a session runs joins it. A wake for a type the running pass has
///   not reached yet is synced by that pass. A wake for a type it already read (new data since)
///   makes one follow-up pass over just those types. More wakes after that start a new session.
/// - Each wake's HealthKit completion is called once its type was read after the wake (or when
///   the session ends without reading it: unreadable type, failure, out of background time).
/// - `onActive` says when a session starts and ends: the UI shows one sync for all of it.
final class SyncSessions: @unchecked Sendable {
    /// One pass over the types. `only` nil: every type, else those types only.
    struct Pass: Sendable, Equatable {
        var trigger: SyncTrigger
        var allAux: Bool
        var only: Set<String>?
    }

    struct PassResult: Sendable {
        var uploaded: Int
        var failed: Bool
    }

    /// What a session did, for its heartbeat.
    struct Summary: Sendable {
        /// The first trigger; every batch of the session carries it.
        var trigger: SyncTrigger
        /// Every trigger that joined, in order, the first included.
        var triggers: [SyncTrigger]
        var passes: Int
        var uploaded: Int
        /// When the first pass started.
        var started: Date
    }

    typealias Run = @Sendable (Pass) async -> PassResult
    typealias Ended = @Sendable (Summary) async -> Void

    private struct Waiter {
        let resume: CheckedContinuation<Void, Never>
        let done: CompletionOnce?
    }

    let debounce: Duration
    private let onActive: @Sendable (Bool) -> Void
    private let lock = NSLock()
    private var session: Task<Void, Never>?
    private var timer: Task<Void, Never>?
    private var active = false
    private var triggers: [SyncTrigger] = []
    private var allAux = false
    /// The pass running, nil between passes.
    private var passing: Pass?
    /// Types the running pass started reading.
    private var started = Set<String>()
    /// Types with a wake since the running pass started reading them: a follow-up pass.
    private var dirty = Set<String>()
    /// A trigger other than a wake joined while a pass ran.
    private var joined = false
    /// Wakes not covered yet, by type; moved to `reading` when their type's read starts.
    private var waiting: [String: [CompletionOnce]] = [:]
    private var reading: [String: [CompletionOnce]] = [:]
    private var waiters: [Waiter] = []

    init(debounce: Duration = .milliseconds(1500), onActive: @escaping @Sendable (Bool) -> Void = { _ in }) {
        self.debounce = debounce
        self.onActive = onActive
    }

    /// Asks for a sync and returns when the session that serves it ends (for a wake: the
    /// session that read its type). `key` and `done`: a HealthKit wake for that type.
    func request(_ trigger: SyncTrigger, allAux: Bool, key: String? = nil, done: CompletionOnce? = nil,
                 run: @escaping Run, ended: @escaping Ended) async {
        await withCheckedContinuation { (cont: CheckedContinuation<Void, Never>) in
            lock.withLock {
                if !triggers.contains(trigger) { triggers.append(trigger) }
                if passing == nil { self.allAux = self.allAux || allAux }
                if let key {
                    if let done { waiting[key, default: []].append(done) }
                    if let pass = passing, started.contains(key) || pass.only.map({ !$0.contains(key) }) ?? false {
                        dirty.insert(key)
                    }
                } else if passing != nil {
                    joined = true
                }
                waiters.append(Waiter(resume: cont, done: done))
                if session == nil {
                    start(debounced: key != nil, run: run, ended: ended)
                } else if key == nil {
                    timer?.cancel()  // waiting for more wakes: start now
                }
            }
        }
    }

    /// The running pass starts reading `key`: the wakes for it so far are covered.
    func willSync(_ key: String) {
        lock.withLock {
            started.insert(key)
            if let w = waiting.removeValue(forKey: key) { reading[key, default: []] += w }
        }
    }

    /// The running pass is done with `key` (read or failed): answers HealthKit for it.
    func didSync(_ key: String) {
        let done = lock.withLock { reading.removeValue(forKey: key) ?? [] }
        done.forEach { $0.call() }
    }

    /// Stops the session, if any; returns whether there was one. Its open wakes are answered.
    @discardableResult
    func cancel() -> Bool {
        lock.withLock {
            timer?.cancel()
            session?.cancel()
            return session != nil
        }
    }

    var isActive: Bool { lock.withLock { session != nil } }

    /// Under the lock.
    private func start(debounced: Bool, run: @escaping Run, ended: @escaping Ended) {
        if !active {
            active = true
            onActive(true)
        }
        let wait = debounce
        timer = debounced ? Task { try? await Task.sleep(for: wait) } : nil
        // The task's first step waits for this lock, so `session` is set before it runs.
        session = Task { await self.loop(run: run, ended: ended) }
    }

    /// Under the lock.
    private func begin(_ pass: Pass) -> Pass {
        passing = pass
        started = []
        dirty = []
        joined = false
        return pass
    }

    private func loop(run: @escaping Run, ended: @escaping Ended) async {
        let wait = lock.withLock { timer }
        await wait?.value
        let first = Date.now
        var pass = lock.withLock { () -> Pass in
            timer = nil
            return begin(Pass(trigger: triggers.first ?? .foreground, allAux: allAux, only: nil))
        }
        var passes = 0, uploaded = 0, failed = false
        while true {
            let result = Task.isCancelled ? PassResult(uploaded: 0, failed: true) : await run(pass)
            passes += 1
            uploaded += result.uploaded
            failed = result.failed
            let next = lock.withLock { () -> Pass? in
                let dirtyNow = dirty, joinedNow = joined
                passing = nil
                guard !Task.isCancelled, passes < 2 else { return nil }
                if failed {
                    // Locked or unreachable: again only if something came in meanwhile (an unlock).
                    return joinedNow ? begin(Pass(trigger: pass.trigger, allAux: false, only: nil)) : nil
                }
                return dirtyNow.isEmpty ? nil : begin(Pass(trigger: pass.trigger, allAux: false, only: dirtyNow))
            }
            guard let next else { break }
            pass = next
        }
        let cancelled = Task.isCancelled
        let (summary, answer, resume) = lock.withLock { () -> (Summary, [CompletionOnce], [CheckedContinuation<Void, Never>]) in
            let summary = Summary(trigger: triggers.first ?? pass.trigger, triggers: triggers, passes: passes,
                                  uploaded: uploaded, started: first)
            // New data the last pass did not read: a new session reads it.
            let carry = cancelled || failed ? [] : dirty
            var carried: [String: [CompletionOnce]] = [:]
            for key in carry { carried[key] = waiting.removeValue(forKey: key) }
            let answer = waiting.values.flatMap { $0 } + reading.values.flatMap { $0 }
            // Callers whose wake was carried wait for the new session.
            let keep = Set(carried.values.flatMap { $0 }.map(ObjectIdentifier.init))
            let isCarried = { (w: Waiter) in w.done.map { keep.contains(ObjectIdentifier($0)) } ?? false }
            let resume = waiters.filter { !isCarried($0) }.map(\.resume)
            waiters = waiters.filter(isCarried)
            waiting = carried.filter { !$0.value.isEmpty }
            reading = [:]
            triggers = []
            allAux = false
            started = []
            dirty = []
            joined = false
            session = nil
            timer = nil
            if carry.isEmpty {
                active = false
                onActive(false)
            } else {
                triggers = [.observer]
                start(debounced: true, run: run, ended: ended)
            }
            return (summary, answer, resume)
        }
        answer.forEach { $0.call() }
        await ended(summary)
        resume.forEach { $0.resume() }
    }
}

/// What a background run did, sent to the server (POST /v1/heartbeat, server 0.6.2+). No health
/// data: triggers, states, counts, times and error messages.
struct AppHeartbeat: Encodable, Sendable {
    var trigger: String
    var appState: String
    var protectedData: Bool
    var outcome: String
    var error: String?
    var lastError: String?
    var pending: String?
    var uploaded: Int
    var seconds: Double
    /// -1: in the foreground (no limit).
    var backgroundTimeLeft: Double
    var processAge: Int
    var appVersion: String
    var wakes: [String: WakeCount]

    static let encoder: JSONEncoder = {
        let e = JSONEncoder()
        e.keyEncodingStrategy = .convertToSnakeCase
        e.dateEncodingStrategy = .iso8601
        e.outputFormatting = .sortedKeys
        return e
    }()
}

/// How often one trigger woke this process, and when last.
struct WakeCount: Encodable, Sendable, Equatable {
    var count: Int
    var last: Date
}
