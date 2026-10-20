import Foundation
import HealthKit
import UIKit

enum SyncError: LocalizedError {
    case locked
    case disallowed(String)
    case notAuthorized
    case noAnchor
    case noAccess

    var errorDescription: String? {
        switch self {
        case .locked: "Health data cannot be read while this iPhone is locked. Health Relay syncs after you unlock it."
        case let .disallowed(reason): "Health refused the request: \(reason)"
        case .notAuthorized: "Health Relay has no access to this data in Health."
        case .noAnchor: "Health gave an incomplete answer. Health Relay tries again at the next sync."
        case .noAccess: "Health Relay cannot read any Health data yet. Pull down to try again, or allow access in Settings › Privacy & Security › Health › Health Relay."
        }
    }
}

/// What the UI shows. Updated by the engine on the main actor.
@MainActor
final class SyncStatus: ObservableObject {
    static let shared = SyncStatus()

    @Published var running = false
    @Published var detail = ""
    /// Share of the data types done in the running sync, 0 to 1.
    @Published var progress: Double?
    /// iOS 27+: Health shares only data since this date (the user chose recent history).
    @Published var historyLimit: Date?
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

/// Which types the app reads, and why the others are left out.
struct AuthPlan: @unchecked Sendable {
    /// Requested in one authorization sheet.
    var read: Set<HKObjectType> = []
    /// Types the user grants object by object (vision prescriptions...), one sheet each.
    var perObject: [HKObjectType] = []
    /// Types an app may not request but can read once their parts are granted: correlations
    /// (their member quantities), medication dose events (the medications).
    var derived: [HKObjectType] = []
    /// Health Records (clinical) types. Requested only when the user opts in: asking for them
    /// opens Apple's provider-setup flow, which is a dead end without a connected provider, and
    /// HealthKit cannot tell an app whether a provider is connected.
    var healthRecords: Set<HKObjectType> = []
    /// Types HealthKit only accepts requested together with others: route -> workout.
    var companions: [String: [HKObjectType]] = [:]
    /// Identifier -> reason it cannot be requested (HealthKit forbids it, unsupported device...).
    var excluded: [String: String] = [:]
    /// False when HealthKit could not answer for some types (phone locked): not to be kept.
    var complete = true
}

/// Counts for the UI.
struct Coverage: Sendable {
    var sdk = SDKHealthTypes.sdk
    var inSDK = 0
    var available = 0
    var requested = 0
    var excluded: [String: String] = [:]
    var unavailable: [String] = []
    /// Identifiers of the types read.
    var readable: [String] = []
    /// Types of the main request never asked (an update added them).
    var notYetRequested = 0
}

/// How a per-object request (one optional row each) ended.
enum PerObjectOutcome: Codable, Equatable, Sendable {
    /// The user went through Health's sheet; this many objects are readable now (nil: unknown).
    case answered(Int?)
    /// Health did not answer in time. A sheet still open can still answer later.
    case noAnswer
    case failed(String)

    var text: String {
        switch self {
        case .answered(nil): "Done"
        case .answered(0): "None shared"
        case let .answered(n?): "\(n) shared"
        case .noAnswer: "No answer from Health: tap to ask again"
        case let .failed(reason): "Could not ask: \(reason)"
        }
    }
}

/// One optional per-object row: vision prescriptions, medications, CDA documents.
struct PerObjectRow: Identifiable, Sendable {
    let identifier: String
    let name: String
    let outcome: PerObjectOutcome?
    var id: String { identifier }
}

/// Incremental sync of everything in HealthKit.
///
/// Every sample type gets an HKAnchoredObjectQuery resumed from a persisted anchor, so each run
/// uploads only new samples and deletions; a type never synced starts from nil, which is a full
/// backfill. An anchor advances only after the server stored the whole page, so nothing is lost
/// on failure and an interrupted backfill resumes where it stopped; the server drops
/// duplicates. Things without anchors (characteristics, activity summaries, medications) are
/// re-read every sync and de-duplicated by content on the server.
actor SyncEngine {
    static let shared = SyncEngine()

    let store = HKHealthStore()
    private let anchors = AnchorStore()
    private let units: UnitResolver
    /// Upper bound per POST; a single item may exceed it (it is then sent alone).
    private let maxBatchBytes = 4 << 20
    private let maxBatchItems = 5000

    private var inFlight: [String: Task<Int, Error>] = [:]
    private var busy = false
    private var waiters: [CheckedContinuation<Void, Never>] = []
    let observers = Observers()
    private var planTask: Task<AuthPlan, Never>?
    private var requestedTask: Task<(Set<String>, Bool), Never>?
    private var lastAux: Date?
    /// A full sync still owed, see PendingSync. Also kept here: before the first unlock after
    /// a restart, UserDefaults can neither be read nor written.
    private var pendingInMemory: PendingSync?
    /// Every trigger shares one sync at a time; the UI shows it as one.
    nonisolated let sessions = SyncSessions { active in
        DispatchQueue.main.async { MainActor.assumeIsolated { SyncStatus.shared.running = active } }
    }
    private var wakeLoggedAt = Date.distantPast
    private var wakesNotLogged = 0
    /// For heartbeats: this process's wakes by trigger, its start, how the last sync ended and
    /// the last error not reported yet (a heartbeat that could not be sent).
    private var wakes: [String: WakeCount] = [:]
    private let processStart = Date.now
    private var lastFailure: Error?
    private var lastError: String?

    var pending: PendingSync? { pendingInMemory ?? PendingSync.load() }

    private func markPending(_ reason: PendingSync) {
        let merged = PendingSync.merge(pending, reason)
        pendingInMemory = merged
        PendingSync.save(merged)
    }

    private func clearPending() {
        pendingInMemory = nil
        PendingSync.save(nil)
    }

    static var isAvailable: Bool { HKHealthStore.isHealthDataAvailable() }

    init() {
        units = UnitResolver(store: store)
        // App 0.2.3 recorded per-object types as asked before asking (see perObjectKey).
        UserDefaults.standard.removeObject(forKey: "perObjectRequested")
    }

    // MARK: Authorization

    /// Sorts every available type into one authorization request, per-object requests and
    /// exclusions. HealthKit raises an exception (not an error) for types an app may not
    /// request, so each type is probed on its own first, without showing anything.
    func authPlan() async -> AuthPlan {
        // Shared by concurrent callers (launch observers, the UI), computed once per launch
        // if HealthKit answered every probe. A plan from a locked phone is not kept: the next
        // call (unlock, the app opening) computes it again.
        if let planTask { return await planTask.value }
        let task = Task { await self.computePlan() }
        planTask = task
        let p = await task.value
        if !p.complete, planTask == task { planTask = nil }
        return p
    }

    private func computePlan() async -> AuthPlan {
        let started = Date.now
        var p = AuthPlan()
        let readable = await MainActor.run { UIApplication.shared.isProtectedDataAvailable }
        let healthRecords = store.supportsHealthRecords()
        for synced in HealthTypes.all {
            let type = synced.type
            if type is HKClinicalType {
                if healthRecords {
                    p.healthRecords.insert(type)
                } else {
                    p.excluded[type.identifier] = "Health Records not supported on this device"
                }
                continue
            }
            if #available(iOS 16.0, *), type.requiresPerObjectAuthorization() {
                p.perObject.append(type)
                continue
            }
            guard let reason = await probe([type]) else {
                p.read.insert(type)
                continue
            }
            if reason == Self.inaccessible {
                p.complete = false
                continue
            }
            // Some types must be requested with others ("Authorization for
            // HKWorkoutTypeIdentifier should also be requested when requesting ... route").
            let companions = Self.identifiers(in: reason).filter { $0 != type.identifier }.compactMap { HealthTypes.named($0)?.type }
            if !companions.isEmpty, await probe([type] + companions) == nil {
                p.read.insert(type)
                p.read.formUnion(companions)
                p.companions[type.identifier] = companions
                continue
            }
            if reason.contains("disallowed") && type is HKSampleType {
                p.derived.append(type)
            } else {
                p.excluded[type.identifier] = reason
            }
        }
        let stillReadable = await MainActor.run { UIApplication.shared.isProtectedDataAvailable }
        if !readable || !stillReadable || p.read.isEmpty { p.complete = false }
        Log.info("authorization: plan in \(Self.elapsed(since: started)): \(p.read.count) in the main request, "
            + "per-object \(p.perObject.map(\.identifier)), \(p.derived.count) derived, \(p.healthRecords.count) health records, "
            + "\(p.excluded.count) excluded" + (p.complete ? "" : "; incomplete (phone locked?), computed again next time"))
        return p
    }

    /// What probe returns when HealthKit's database cannot be read (the phone is locked).
    private static let inaccessible = "HealthKit database inaccessible"

    static func elapsed(since start: Date) -> String {
        String(format: "%.1f s", Date.now.timeIntervalSince(start))
    }

    private static func identifiers(in text: String) -> [String] {
        text.matches(of: #/HK[A-Za-z0-9]+/#).map { String($0.output) }
    }

    /// statusForAuthorizationRequest, nil instead of an exception.
    private func requestStatus(_ types: Set<HKObjectType>) async -> HKAuthorizationRequestStatus? {
        await withCheckedContinuation { (cont: CheckedContinuation<HKAuthorizationRequestStatus?, Never>) in
            let once = ResumeOnce(cont)
            let raised = HKRTry {
                self.store.getRequestStatusForAuthorization(toShare: [], read: types) { status, error in
                    once.resume(error == nil ? status : nil)
                }
            }
            if raised != nil { once.resume(nil) }
        }
    }

    private func probe(_ types: [HKObjectType]) async -> String? {
        await withCheckedContinuation { (cont: CheckedContinuation<String?, Never>) in
            let once = ResumeOnce(cont)
            let raised = HKRTry {
                self.store.getRequestStatusForAuthorization(toShare: [], read: Set(types)) { _, error in
                    if let e = error as? HKError, e.code == .errorDatabaseInaccessible {
                        once.resume(Self.inaccessible)
                    } else {
                        once.resume(error?.localizedDescription)
                    }
                }
            }
            if let raised { once.resume(raised) }
        }
    }

    /// The one authorization sheet for every type that has no other flow. HealthKit shows it
    /// only for types never answered, so with everything answered this returns at once.
    /// Per-object types (vision prescriptions, medications...) are not part of it: their
    /// sheets can take long or never come, so each is asked from its own row, see
    /// requestPerObject.
    /// Returns whether Health had anything to ask.
    @discardableResult
    func requestAuthorization() async throws -> Bool {
        let started = Date.now
        let p = await authPlan()
        let before = await requestStatus(p.read)
        Log.info("authorization: main request, \(p.read.count) types, status before: \(Self.statusName(before))")
        // Saved before asking: launches observe these without asking HealthKit first.
        ObservedTypes.add(p.read.map(\.identifier) + p.derived.map(\.identifier))
        try await withCheckedThrowingContinuation { (cont: CheckedContinuation<Void, Error>) in
            let once = ResumeOnceThrowing(cont)
            let raised = HKRTry {
                self.store.requestAuthorization(toShare: [], read: p.read) { _, error in
                    if let error { once.fail(error) } else { once.succeed(()) }
                }
            }
            if let raised { once.fail(SyncError.disallowed(raised)) }
        }
        UserDefaults.standard.set(p.read.map(\.identifier), forKey: Self.askedReadKey)
        Log.info("authorization: main request done in \(Self.elapsed(since: started)), status after: \(Self.statusName(await requestStatus(p.read)))")
        accessChanged()
        return before != .unnecessary
    }

    private static func statusName(_ status: HKAuthorizationRequestStatus?) -> String {
        guard let status else { return "unknown (HealthKit refused)" }
        return HealthTypes.enumName("HKAuthorizationRequestStatus", status.rawValue) ?? "\(status.rawValue)"
    }

    /// Readable types changed: recompute them, and register observers for new ones without
    /// making the caller (a button) wait for that.
    func accessChanged() {
        requestedTask = nil
        Task { await self.startObserving() }
    }

    // MARK: History limit (iOS 27)

    private static let historyLimitKey = "historyLimit"

    /// The latest "earliest readable date" Health sets for any read type when the user shares
    /// only recent history; nil with full access or before iOS 27.
    func historyLimit() async -> Date? {
        guard #available(iOS 27.0, *) else { return nil }
        let types = Set((await authPlan()).read)
        let store = store
        return await withCheckedContinuation { (cont: CheckedContinuation<Date?, Never>) in
            let once = ResumeOnce(cont)
            let raised = HKRTry {
                store.getEarliestAuthorizedSampleDate(for: types) { dates, _ in once.resume(dates?.values.max()) }
            }
            if raised != nil { once.resume(nil) }
        }
    }

    /// Anchored queries never return samples from before a limit that is later lifted (their
    /// anchors are past them), so when the user widens access, history is read again; the
    /// server skips what it has.
    private func checkHistoryLimit() async {
        let limit = await historyLimit()
        let defaults = UserDefaults.standard
        let previous = defaults.object(forKey: Self.historyLimitKey) as? Date
        if let previous, limit == nil || limit! < previous {
            Log.info("sync: Health now shares older data (limit was \(previous.formatted(.iso8601.year().month().day()))): reading all history again")
            anchors.reset()
        }
        if let limit {
            defaults.set(limit, forKey: Self.historyLimitKey)
            if limit != previous { Log.info("sync: Health shares data since \(limit.formatted(.iso8601.year().month().day())) only") }
        } else {
            defaults.removeObject(forKey: Self.historyLimitKey)
        }
        await MainActor.run { SyncStatus.shared.historyLimit = limit }
    }

    // MARK: Per-object authorization

    static let perObjectKey = "perObjectOutcomes"
    /// Long enough for a sheet that is slow to come up; the request goes on after it, see
    /// PerObjectOutcome.noAnswer.
    static let perObjectTimeout: Double = 45
    static let perObjectChanged = Notification.Name("HealthRelayPerObjectChanged")

    private var perObjectOutcomes: [String: PerObjectOutcome] {
        get {
            guard let data = UserDefaults.standard.data(forKey: Self.perObjectKey) else { return [:] }
            return (try? JSONDecoder().decode([String: PerObjectOutcome].self, from: data)) ?? [:]
        }
        set {
            UserDefaults.standard.set(try? JSONEncoder().encode(newValue), forKey: Self.perObjectKey)
        }
    }

    /// The optional rows to show: per-object types this iPhone has, with the last outcome.
    func perObjectRows() async -> [PerObjectRow] {
        let outcomes = perObjectOutcomes
        return await authPlan().perObject.map {
            PerObjectRow(identifier: $0.identifier, name: Self.perObjectName($0.identifier), outcome: outcomes[$0.identifier])
        }
    }

    static func perObjectName(_ identifier: String) -> String {
        switch identifier {
        case HKVisionPrescriptionTypeIdentifier: "Vision prescriptions"
        case HKDocumentTypeIdentifier.CDA.rawValue: "Clinical documents (CDA)"
        case "HKDataTypeUserAnnotatedMedicationConcept", "HKDataTypeIdentifierUserAnnotatedMedicationConcept": "Medications"
        default: shortName(identifier)
        }
    }

    /// Asks for one per-object type: Health shows a sheet listing its objects, the user picks
    /// which to share. Returns within perObjectTimeout; a later answer is still recorded.
    func requestPerObject(_ identifier: String) async -> PerObjectOutcome {
        guard #available(iOS 16.0, *),
              let type = await authPlan().perObject.first(where: { $0.identifier == identifier })
        else { return .failed("not available on this iPhone") }
        let started = Date.now
        Log.info("authorization: per-object request for \(identifier)")
        ObservedTypes.add([identifier])
        let store = store
        let answer: String?? = await withTimeout(Self.perObjectTimeout, late: { error in
            Log.info("authorization: \(identifier) answered late, after \(Self.elapsed(since: started))")
            Task { await SyncEngine.shared.recordPerObject(identifier, type, error: error) }
        }) { (done: @escaping @Sendable (String?) -> Void) in
            let raised = HKRTry {
                store.requestPerObjectReadAuthorization(for: type, predicate: nil) { _, error in done(error?.localizedDescription) }
            }
            if let raised { done(raised) }
        }
        guard let error = answer else {
            Log.info("authorization: \(identifier): no answer after \(Self.elapsed(since: started))")
            perObjectOutcomes[identifier] = .noAnswer
            accessChanged()
            return .noAnswer
        }
        Log.info("authorization: \(identifier) answered in \(Self.elapsed(since: started))\(error.map { ": \($0)" } ?? "")")
        return await recordPerObject(identifier, type, error: error)
    }

    @discardableResult
    private func recordPerObject(_ identifier: String, _ type: HKObjectType, error: String?) async -> PerObjectOutcome {
        let outcome: PerObjectOutcome
        if let error {
            outcome = .failed(error)
        } else {
            let n = await sharedCount(type)
            Log.info("authorization: \(identifier): \(n.map(String.init) ?? "unknown number of") objects readable")
            outcome = .answered(n)
        }
        perObjectOutcomes[identifier] = outcome
        accessChanged()
        await MainActor.run { NotificationCenter.default.post(name: Self.perObjectChanged, object: nil) }
        return outcome
    }

    /// How many objects of a per-object type the app can read now; nil if HealthKit does not
    /// say within 10 s.
    private func sharedCount(_ type: HKObjectType) async -> Int? {
        let store = store
        if let sampleType = type as? HKSampleType {
            return await withTimeout(10) { (done: @escaping @Sendable (Int?) -> Void) in
                Task { done((try? await store.safeSamples(sampleType))?.count) }
            } ?? nil
        }
        if #available(iOS 26.0, *), type == HKObjectType.userAnnotatedMedicationType() {
            return await withTimeout(10) { (done: @escaping @Sendable (Int?) -> Void) in
                Task { done(try? await Self.medications(store).count) }
            } ?? nil
        }
        return nil
    }

    @available(iOS 26.0, *)
    static func medications(_ store: HKHealthStore) async throws -> [HKUserAnnotatedMedication] {
        let stream: AsyncThrowingStream<HKUserAnnotatedMedication, Error> = store.safeStream { cont in
            HKUserAnnotatedMedicationQuery(predicate: nil, limit: HKObjectQueryNoLimit) { _, medication, done, error in
                if let error { cont.finish(throwing: error); return }
                if let medication { cont.yield(medication) }
                if done { cont.finish() }
            }
        }
        var out: [HKUserAnnotatedMedication] = []
        for try await m in stream { out.append(m) }
        return out
    }

    static let healthRecordsOptInKey = "healthRecordsOptIn"

    /// Whether the user chose to include Health Records.
    var healthRecordsOptedIn: Bool { UserDefaults.standard.bool(forKey: Self.healthRecordsOptInKey) }

    /// Asks for Health Records access separately (opt-in, see `AuthPlan.healthRecords`).
    func requestHealthRecords() async throws {
        let p = await authPlan()
        guard !p.healthRecords.isEmpty else { return }
        ObservedTypes.add(p.healthRecords.map(\.identifier))
        try await withCheckedThrowingContinuation { (cont: CheckedContinuation<Void, Error>) in
            let once = ResumeOnceThrowing(cont)
            let raised = HKRTry {
                self.store.requestAuthorization(toShare: [], read: p.healthRecords) { _, error in
                    if let error { once.fail(error) } else { once.succeed(()) }
                }
            }
            if let raised { once.fail(SyncError.disallowed(raised)) }
        }
        UserDefaults.standard.set(true, forKey: Self.healthRecordsOptInKey)
        accessChanged()
    }

    /// Whether Health Records can be offered on this device.
    func healthRecordsAvailable() async -> Bool { !(await authPlan()).healthRecords.isEmpty }

    /// Whether the authorization sheet still needs to be shown: true after an update brings
    /// new types. HealthKit never reveals which read permissions were granted.
    ///
    /// Tracked by what this app already asked for, not by HealthKit's request status: some types
    /// can report "should request" even right after the sheet was shown, which would leave the
    /// app stuck asking forever.
    func needsAuthorization() async -> Bool {
        let p = await authPlan()
        let askedRead = Set(UserDefaults.standard.stringArray(forKey: Self.askedReadKey) ?? [])
        return !Set(p.read.map(\.identifier)).isSubset(of: askedRead)
    }

    static let askedReadKey = "readRequested"

    /// Types the user has been asked about. Only those sync: an anchored query over a type
    /// never requested returns nothing, and its anchor must not advance past real history.
    private func requestedTypes() async -> Set<String> {
        // Kept for the process only when HealthKit answered for every type: an empty or
        // partial answer from a locked phone would otherwise stop syncing until the app is
        // ended.
        if let requestedTask { return await requestedTask.value.0 }
        let task = Task { await self.computeRequested() }
        requestedTask = task
        let (out, reliable) = await task.value
        if !reliable, requestedTask == task { requestedTask = nil }
        return out
    }

    private func computeRequested() async -> (Set<String>, Bool) {
        let p = await authPlan()
        var reliable = p.complete
        var out = Set<String>()
        for type in p.read {
            let set = Set([type] + (p.companions[type.identifier] ?? []))
            switch await requestStatus(set) {
            case .unnecessary?: out.insert(type.identifier)
            case nil: reliable = false
            default: break
            }
        }
        // Asked from their row, whatever the outcome: reading a type nothing was shared of
        // just returns nothing.
        let asked = perObjectOutcomes
        for type in p.perObject where asked[type.identifier] != nil { out.insert(type.identifier) }
        if healthRecordsOptedIn {
            for type in p.healthRecords where await requestStatus([type]) == .unnecessary {
                out.insert(type.identifier)
            }
        }
        if !out.isEmpty {
            for type in p.derived { out.insert(type.identifier) }
        }
        let readable = await MainActor.run { UIApplication.shared.isProtectedDataAvailable }
        if !readable || out.isEmpty { reliable = false }
        // Installs from before 0.6.1 never saved what they asked for: this is it.
        if reliable { ObservedTypes.add(out) }
        return (out, reliable)
    }

    func coverage() async -> Coverage {
        let p = await authPlan()
        let req = await requestedTypes()
        let catalog = HealthTypes.catalog
        let askedRead = Set(UserDefaults.standard.stringArray(forKey: Self.askedReadKey) ?? [])
        return Coverage(
            inSDK: SDKHealthTypes.identified.count + SDKHealthTypes.factories.count,
            available: catalog.types.count,
            requested: req.count,
            excluded: p.excluded,
            unavailable: catalog.unavailable,
            readable: req.sorted(),
            notYetRequested: p.read.filter { !askedRead.contains($0.identifier) }.count)
    }

    // MARK: Background delivery

    /// Starts the observer queries of the types asked about, from what was saved when asking:
    /// no HealthKit call first, so it can run synchronously while the app launches, as
    /// HealthKit requires for background delivery (late or missing observers make it stop
    /// waking the app).
    nonisolated func observeAtLaunch() {
        guard Self.isAvailable else { return }
        let types = ObservedTypes.atLaunch()
        observers.observe(types, store: store)
        let (observed, _) = observers.counts
        Log.info("observe: \(observed) types observed at launch" + (ObservedTypes.load() == nil ? " (all: asked types unknown)" : ""))
    }

    /// Adds observers and background delivery for readable types not observed yet, from
    /// HealthKit's current answers. Runs after launch, at unlock and when the app opens.
    func startObserving() async {
        guard Self.isAvailable else { return }
        let req = await requestedTypes()
        observers.observe(HealthTypes.all.filter { $0.sampleType != nil && req.contains($0.key) }, store: store)
        let (observed, delivery) = observers.counts
        Log.info("observe: \(observed) types observed, \(delivery) with background delivery, \(req.count) readable")
    }

    /// HealthKit stops waking an app that leaves its observer calls unanswered, so each is
    /// answered once its type was read after it, after 20 s, or when background time runs out
    /// (backgroundTimeOver), whichever comes first. All wakes share one full incremental sync
    /// (SyncSessions, which waits a moment for the rest of a burst): a burst of a few hundred
    /// wakes (every query fires once when it starts) costs one sync, not one per type, and any
    /// wake reads every type, so a single wake keeps everything current.
    func observerFired(_ key: String, done: CompletionOnce) async {
        noteWake(.observer)
        if Date.now.timeIntervalSince(wakeLoggedAt) >= 30 {
            let state = await AppState.now
            Log.info("observe: wake \(Self.shortName(key)), \(state.description)"
                + (wakesNotLogged > 0 ? " (\(wakesNotLogged) more wakes since the last such line)" : ""))
            wakeLoggedAt = .now
            wakesNotLogged = 0
        } else {
            wakesNotLogged += 1
        }
        Task {
            try? await Task.sleep(for: .seconds(20))
            done.call()
        }
        await syncAll(trigger: .observer, allAux: false, wake: key, done: done)
        done.call()
    }

    /// iOS is about to suspend the app: answer HealthKit and stop the observer sync, which
    /// leaves the rest owed (.failed) for the next wake. Synchronous, from the expiration
    /// handler: anything later may run only when iOS resumes the app, hours later.
    nonisolated func backgroundTimeOver() {
        let answered = observers.answerAll()
        let stopped = sessions.cancel()
        if answered > 0 || stopped {
            Log.info("app: background time over: answered \(answered) HealthKit wakes" + (stopped ? ", stopped the sync" : ""))
        }
    }

    private func noteWake(_ trigger: SyncTrigger) {
        var w = wakes[trigger.rawValue] ?? WakeCount(count: 0, last: .now)
        w.count += 1
        w.last = .now
        wakes[trigger.rawValue] = w
    }

    /// Tells the server what a background run did (POST /v1/heartbeat), also when it could read
    /// or send nothing, so a sync that stops can be diagnosed from the server alone. One try:
    /// a failure is logged and goes with the next heartbeat.
    func report(_ trigger: SyncTrigger, uploaded: Int, started: Date, outcome: String? = nil, error: String? = nil) async {
        let settings = ServerSettings.load()
        guard settings.isComplete else { return }
        let (state, left) = await MainActor.run { (AppState.now, UIApplication.shared.backgroundTimeRemaining) }
        let info = Bundle.main.infoDictionary
        let failure = lastFailure
        let hb = AppHeartbeat(
            trigger: trigger.rawValue, appState: state.state, protectedData: state.protectedData,
            outcome: outcome ?? Self.outcome(failure), error: error ?? failure?.localizedDescription,
            lastError: lastError, pending: pending?.rawValue, uploaded: uploaded,
            seconds: (Date.now.timeIntervalSince(started) * 10).rounded() / 10,
            backgroundTimeLeft: left > 1e6 ? -1 : left.rounded(),
            processAge: Int(Date.now.timeIntervalSince(processStart)),
            appVersion: "\(info?["CFBundleShortVersionString"] as? String ?? "?") (\(info?["CFBundleVersion"] as? String ?? "?"))",
            wakes: wakes)
        do {
            try await Uploader(settings: settings).heartbeat(hb)
            lastError = nil
        } catch {
            lastError = "heartbeat not sent: \(error.localizedDescription)"
            Log.info("heartbeat: not sent (\(trigger.rawValue)): \(error.localizedDescription)")
        }
    }

    /// A run's outcome, in a word, for heartbeats.
    static func outcome(_ failure: Error?) -> String {
        guard let failure else { return "ok" }
        switch failure {
        case is CancellationError: return "cancelled"
        case SyncError.locked: return "locked"
        case UploadError.failed: return "upload failed"
        case UploadError.unauthorized: return "unauthorized"
        case UploadError.notConfigured: return "not configured"
        default: return "failed"
        }
    }

    /// Remembers to sync again later after an error that may pass (the lock is noted where it
    /// is detected).
    private func notePending(_ error: Error) {
        switch error {
        case UploadError.failed, is CancellationError: markPending(.failed)
        default: break
        }
    }

    /// Protected data became readable (the phone was unlocked), or the app opened: compute
    /// again what a locked launch could not, observe what is new, and run a sync still owed.
    /// Returns whether a sync was owed (and run).
    @discardableResult
    func dataBecameAvailable(_ trigger: SyncTrigger) async -> Bool {
        if trigger != .foreground { noteWake(trigger) }
        await startObserving()
        guard let pending else { return false }
        Log.info("sync: making up for a \(pending == .locked ? "skipped (locked)" : "failed") sync")
        await syncAll(trigger: pending.trigger(trigger))
        return true
    }

    /// The background app refresh: a full incremental sync if HealthKit can be read, else
    /// one owed for the next unlock. Returns whether nothing is left owed.
    func backgroundRefresh() async -> Bool {
        let state = await AppState.now
        Log.info("refresh: woke, \(state.description)")
        noteWake(.refresh)
        let started = Date.now
        guard ServerSettings.load().isComplete else { return true }
        guard state.protectedData else {
            markPending(.locked)
            await report(.refresh, uploaded: 0, started: started, outcome: "locked", error: "")
            return true
        }
        await startObserving()
        await syncAll(trigger: (pending ?? .locked).trigger(.refresh))
        return pending == nil
    }

    // MARK: Sync

    /// Syncs everything, or joins the sync running (see SyncSessions); returns when that
    /// session ends. Every batch carries the trigger of the session's first caller. `allAux`
    /// false re-reads what has no anchor only if it is due (syncAuxIfDue): observer wakes come
    /// often. `wake` and `done`: a HealthKit wake for that type, answered once it was read.
    func syncAll(trigger: SyncTrigger = .foreground, allAux: Bool = true, wake: String? = nil, done: CompletionOnce? = nil) async {
        await sessions.request(trigger, allAux: allAux, key: wake, done: done,
                               run: { pass in await self.runPass(pass) },
                               ended: { summary in await self.sessionEnded(summary) })
    }

    private func runPass(_ pass: SyncSessions.Pass) async -> SyncSessions.PassResult {
        let uploaded = await SyncTrigger.$current.withValue(pass.trigger) {
            await self.runAll(allAux: pass.allAux, only: pass.only)
        }
        return .init(uploaded: uploaded, failed: lastFailure != nil)
    }

    /// One heartbeat per session that a background trigger joined.
    private func sessionEnded(_ s: SyncSessions.Summary) async {
        if s.triggers.count > 1 || s.passes > 1 {
            Log.info("sync: one sync for \(s.triggers.map(\.rawValue).joined(separator: ", ")), \(s.passes) passes")
        }
        if s.triggers.contains(where: { $0 != .foreground }) {
            await report(s.trigger, uploaded: s.uploaded, started: s.started)
        }
    }

    /// One pass: `only` nil reads every type, else just those (new data during the pass before).
    private func runAll(allAux: Bool = true, only: Set<String>? = nil) async -> Int {
        Log.info("sync: started (\(SyncTrigger.current.rawValue))" + (only.map { ", again for \($0.count) types" } ?? ""))
        // This sync is the one owed; a lock or failure in it marks one owed again.
        clearPending()
        await MainActor.run { SyncStatus.shared.lastError = nil }
        var total = 0
        var firstError: Error?
        if only == nil {
            do {
                total += allAux ? try await syncAux() : try await syncAuxIfDue()
            } catch {
                firstError = error
            }
        }
        let req = await requestedTypes()
        await checkHistoryLimit()
        let types = HealthTypes.all.filter { $0.sampleType != nil && req.contains($0.key) && only?.contains($0.key) != false }
        Log.info("sync: \(types.count) readable sample types")
        if types.isEmpty && firstError == nil && only == nil {
            firstError = SyncError.noAccess
        }
        for (i, synced) in types.enumerated() where !Self.isFatal(firstError) {
            // Background time is over (refresh task expired): the next sync goes on from here.
            if Task.isCancelled {
                firstError = CancellationError()
                break
            }
            await show(Self.readableName(synced.key), progress: Double(i) / Double(max(1, types.count)))
            sessions.willSync(synced.key)
            do {
                total += try await sync(synced)
            } catch SyncError.notAuthorized, SyncError.disallowed {
                // read through other types, or access not granted: nothing to report
            } catch {
                firstError = firstError ?? error
            }
            sessions.didSync(synced.key)
        }
        if !Self.isFatal(firstError) {
            do {
                total += try await exclusive("effort") { try await self.syncEffortRelationships() }
            } catch {
                firstError = firstError ?? error
            }
        }
        let uploaded = total
        let failure = firstError
        lastFailure = failure
        if let failure { notePending(failure) } else { Log.info("sync: done, \(uploaded) uploaded") }
        await MainActor.run {
            let status = SyncStatus.shared
            status.detail = ""
            status.progress = nil
            if failure is CancellationError {
                Log.info("sync: stopped, out of background time")
            } else if let failure {
                Log.error("sync: \(failure)")
                status.lastError = failure.localizedDescription
            } else {
                status.lastSuccess = .now
                status.lastUploaded = uploaded
            }
        }
        return total
    }

    /// Locked phone, missing settings or an outdated server affect every type: stop there.
    private static func isFatal(_ error: Error?) -> Bool {
        guard let error else { return false }
        if let e = error as? SyncError, case .locked = e { return true }
        if error is CancellationError { return true }
        if let e = error as? UploadError {
            switch e {
            case .notConfigured, .serverTooOld, .unauthorized: return true
            default: return false
            }
        }
        return false
    }

    /// Syncs one sample type; joins a sync of that type already in progress.
    func sync(_ synced: SyncedType) async throws -> Int {
        guard let type = synced.sampleType else { return 0 }
        return try await exclusive(synced.key) { try await self.run(type) }
    }

    /// One sync at a time: observers fire for many types at once, and HealthKit and the
    /// server both do better with one stream than with dozens.
    private func exclusive(_ key: String, _ body: @escaping @Sendable () async throws -> Int) async throws -> Int {
        if let running = inFlight[key] {
            return try await running.value
        }
        let task = Task {
            await self.acquire()
            defer { self.release() }
            try Task.checkCancellation()
            return try await body()
        }
        inFlight[key] = task
        defer { inFlight[key] = nil }
        // Out of background time (the caller is cancelled): stop the query or upload under way.
        return try await withTaskCancellationHandler { try await task.value } onCancel: { task.cancel() }
    }

    private func acquire() async {
        if !busy {
            busy = true
            return
        }
        await withCheckedContinuation { waiters.append($0) }
    }

    private func release() {
        if waiters.isEmpty {
            busy = false
        } else {
            waiters.removeFirst().resume()
        }
    }

    /// Forgets all sync positions, so the next sync re-sends all history.
    func resetAnchors() async {
        anchors.reset()
    }

    /// Runs `erase` (the server deleting what this iPhone sent) with no sync in between, then
    /// forgets all sync positions. Nothing is reset if `erase` throws. A sync of another type
    /// waits, so no anchor advances past data the server just deleted.
    func resetAnchors(after erase: @escaping @Sendable () async throws -> Void) async throws {
        _ = try await exclusive("erase") {
            try await erase()
            await self.resetAnchors()
            return 0
        }
    }

    private struct Context {
        let uploader: Uploader
        let device: JSON
        let trigger: SyncTrigger
        let appState: String
    }

    private func context() async throws -> Context {
        let settings = ServerSettings.load()
        guard settings.isComplete else { throw UploadError.notConfigured }
        let state = await AppState.now
        guard state.protectedData else {
            markPending(.locked)
            throw SyncError.locked
        }
        return Context(uploader: Uploader(settings: settings), device: await Self.deviceInfo(),
                       trigger: SyncTrigger.current, appState: state.state)
    }

    private func pageLimit(_ type: HKSampleType) -> Int {
        switch type.identifier {
        case HKWorkoutRouteTypeIdentifier: return 5
        case HKDataTypeIdentifierHeartbeatSeries: return 200
        default: break
        }
        switch type {
        case is HKElectrocardiogramType: return 10
        case is HKDocumentType: return 5
        case is HKPrescriptionType: return 10
        case is HKWorkoutType: return 50
        case is HKAudiogramSampleType: return 100
        case is HKCorrelationType: return 500
        default: return 2000
        }
    }

    private func run(_ type: HKSampleType) async throws -> Int {
        let ctx = try await context()
        let encoder = SampleEncoder(store: store, units: units)
        let key = type.identifier
        var total = 0

        while true {
            try Task.checkCancellation()
            let previous = anchors.anchor(for: key)
            let page = try await anchoredPage(type, anchor: previous, limit: pageLimit(type))
            if page.samples.isEmpty && page.deleted.isEmpty {
                // Never anchor a type that has returned nothing yet: if its read access was
                // missing, history must still come when access is granted.
                if previous != nil { try anchors.save(page.anchor, for: key) }
                return total
            }
            var items: [WireItem] = []
            for s in page.samples {
                let out = await encoder.encode(s)
                for r in out.records { items.append(try WireItem(.record, r)) }
                items.append(try WireItem(.sample, out.sample))
            }
            for d in page.deleted {
                items.append(try WireItem(.deletion, WireEncoder.deletion(d, type: type)))
            }
            try await upload(items, ctx)
            try anchors.save(page.anchor, for: key)

            total += items.count
            let shown = total
            await show("\(Self.readableName(key)) · \(shown.formatted()) sent")
        }
    }

    private struct Page: @unchecked Sendable {
        let samples: [HKSample]
        let deleted: [HKDeletedObject]
        let anchor: HKQueryAnchor
    }

    private func anchoredPage(_ type: HKSampleType, anchor: HKQueryAnchor?, limit: Int) async throws -> Page {
        do {
            return try await withCheckedThrowingContinuation { (cont: CheckedContinuation<Page, Error>) in
                let once = ResumeOnceThrowing(cont)
                let raised = store.safeExecute {
                    HKAnchoredObjectQuery(type: type, predicate: nil, anchor: anchor, limit: limit) {
                        _, samples, deleted, newAnchor, error in
                        if let error {
                            once.fail(error)
                        } else if let newAnchor {
                            once.succeed(Page(samples: samples ?? [], deleted: deleted ?? [], anchor: newAnchor))
                        } else {
                            once.fail(SyncError.noAnchor)
                        }
                    }
                }
                if let raised { once.fail(SyncError.disallowed(raised)) }
            }
        } catch let error as HKError where error.code == .errorDatabaseInaccessible {
            markPending(.locked)
            throw SyncError.locked
        } catch let error as HKError where error.code == .errorAuthorizationDenied || error.code == .errorAuthorizationNotDetermined {
            throw SyncError.notAuthorized
        }
    }

    /// Posts items in order, cut into batches by size. Records come before the sample they
    /// belong to, so an archived sample always has its series and files.
    private func upload(_ items: [WireItem], _ ctx: Context) async throws {
        var batch = WireBatch(device: ctx.device, items: [], trigger: ctx.trigger.rawValue, appState: ctx.appState)
        var bytes = 0
        for item in items {
            if !batch.items.isEmpty && (bytes + item.data.count > maxBatchBytes || batch.count >= maxBatchItems) {
                try await ctx.uploader.upload(batch)
                batch.items.removeAll(keepingCapacity: true)
                bytes = 0
            }
            batch.items.append(item)
            bytes += item.data.count + 1
        }
        if !batch.items.isEmpty {
            try await ctx.uploader.upload(batch)
        }
    }

    // MARK: Characteristics, activity summaries, medications, effort relationships

    /// Re-reads what has no anchor at most every 50 minutes (and on every full sync).
    private func syncAuxIfDue() async throws -> Int {
        if let lastAux, Date.now.timeIntervalSince(lastAux) < 50 * 60 { return 0 }
        return try await syncAux()
    }

    private func syncAux() async throws -> Int {
        let total = try await exclusive("aux") {
            let ctx = try await self.context()
            var n = try await self.syncCharacteristics(ctx)
            n += try await self.syncMedications(ctx)
            n += try await self.syncActivitySummaries(ctx)
            return n
        }
        lastAux = .now
        return total
    }

    private func syncCharacteristics(_ ctx: Context) async throws -> Int {
        let req = await requestedTypes()
        var values: [String: JSON] = [:]
        var errors: [String: JSON] = [:]
        func read(_ id: HKCharacteristicTypeIdentifier, _ name: String, _ body: () throws -> JSON) {
            guard req.contains(id.rawValue) else { return }
            var result: Result<JSON, Error>?
            if let raised = HKRTry({ result = Result { try body() } }) { result = .failure(SyncError.disallowed(raised)) }
            switch result {
            case let .success(value)?: values[name] = value
            case let .failure(error)?: errors[name] = .string(error.localizedDescription)
            case nil: break
            }
        }
        read(.dateOfBirth, "date_of_birth") {
            let c = try store.dateOfBirthComponents()
            guard let y = c.year, let m = c.month, let d = c.day else { return .null }
            return .string(String(format: "%04d-%02d-%02d", y, m, d))
        }
        read(.biologicalSex, "biological_sex") { JSON.enumValue(try store.biologicalSex().biologicalSex.rawValue, "HKBiologicalSex") }
        read(.bloodType, "blood_type") { JSON.enumValue(try store.bloodType().bloodType.rawValue, "HKBloodType") }
        read(.fitzpatrickSkinType, "fitzpatrick_skin_type") {
            JSON.enumValue(try store.fitzpatrickSkinType().skinType.rawValue, "HKFitzpatrickSkinType")
        }
        read(.wheelchairUse, "wheelchair_use") { JSON.enumValue(try store.wheelchairUse().wheelchairUse.rawValue, "HKWheelchairUse") }
        read(.activityMoveMode, "activity_move_mode") {
            JSON.enumValue(try store.activityMoveMode().activityMoveMode.rawValue, "HKActivityMoveMode")
        }
        let known: Set<String> = [
            HKCharacteristicTypeIdentifier.dateOfBirth, .biologicalSex, .bloodType, .fitzpatrickSkinType,
            .wheelchairUse, .activityMoveMode,
        ].reduce(into: []) { $0.insert($1.rawValue) }
        let unknown = HealthTypes.all.map(\.key).filter { $0.hasPrefix("HKCharacteristicTypeIdentifier") && !known.contains($0) }
        guard !values.isEmpty || !errors.isEmpty else { return 0 }
        var o: [String: JSON] = ["kind": "characteristics", "key": "characteristics", "values": .object(values)]
        if !errors.isEmpty { o["errors"] = .object(errors) }
        if !unknown.isEmpty { o["not_read"] = .array(unknown.map(JSON.string)) }
        try await upload([try WireItem(.record, .object(o))], ctx)
        return 1
    }

    private func syncMedications(_ ctx: Context) async throws -> Int {
        guard #available(iOS 26.0, *) else { return 0 }
        let type = HKObjectType.userAnnotatedMedicationType()
        guard await requestedTypes().contains(type.identifier) else { return 0 }
        let meds = (try? await Self.medications(store)) ?? []
        var items: [WireItem] = []
        for m in meds {
            let c = m.medication
            var o: [String: JSON] = [
                "kind": "medication",
                "key": .string(String(describing: c.identifier)),
                "is_archived": .bool(m.isArchived),
                "has_schedule": .bool(m.hasSchedule),
                "concept": SampleEncoder.concept(c.identifier),
                "display_text": .string(c.displayText),
                "general_form": .string(c.generalForm.rawValue),
                "related_codings": .array(c.relatedCodings.sorted { $0.code < $1.code }.map { coding in
                    var co: [String: JSON] = ["system": .string(coding.system), "code": .string(coding.code)]
                    co.put("version", JSON(coding.version))
                    return .object(co)
                }),
                "archive": JSON.archive(m),
            ]
            o.put("nickname", JSON(m.nickname))
            items.append(try WireItem(.record, .object(o)))
        }
        try await upload(items, ctx)
        return items.count
    }

    /// Daily rings, from the first day HealthKit could have them to today. The last week is
    /// always re-read, since today's (and late-synced) summaries change.
    private func syncActivitySummaries(_ ctx: Context) async throws -> Int {
        let type = HKObjectType.activitySummaryType()
        guard await requestedTypes().contains(type.identifier) else { return 0 }
        var cal = Calendar(identifier: .gregorian)
        cal.timeZone = .current
        let today = cal.startOfDay(for: .now)
        let positionKey = "activity-summaries"
        var start: Date
        if let s = anchors.value(for: positionKey), let last = Self.day.date(from: s) {
            start = cal.date(byAdding: .day, value: -7, to: last) ?? last
        } else {
            start = cal.date(from: DateComponents(year: 2014, month: 9, day: 1)) ?? today
        }
        var total = 0
        while start <= today {
            let end = min(today, cal.date(byAdding: .day, value: 365, to: start) ?? today)
            var from = cal.dateComponents([.year, .month, .day, .era], from: start)
            var to = cal.dateComponents([.year, .month, .day, .era], from: end)
            from.calendar = cal
            to.calendar = cal
            let summaries: [HKActivitySummary] = try await store.safeResult { answer in
                HKActivitySummaryQuery(predicate: HKQuery.predicate(forActivitySummariesBetweenStart: from, end: to)) { _, summaries, error in
                    if let error { answer(.failure(error)) } else { answer(.success(summaries ?? [])) }
                }
            }
            let items = try summaries.map { try WireItem(.record, activitySummary($0, calendar: cal)) }
            try await upload(items, ctx)
            total += items.count
            // Only move on once something was found: with no access yet, retry from the start.
            if total > 0 || anchors.value(for: positionKey) != nil {
                try anchors.setValue(Self.day.string(from: end), for: positionKey)
            }
            start = cal.date(byAdding: .day, value: 1, to: end) ?? .distantFuture
        }
        return total
    }

    private static let day: DateFormatter = {
        let f = DateFormatter()
        f.calendar = Calendar(identifier: .gregorian)
        f.locale = Locale(identifier: "en_US_POSIX")
        f.dateFormat = "yyyy-MM-dd"
        return f
    }()

    private func activitySummary(_ s: HKActivitySummary, calendar: Calendar) -> JSON {
        let c = s.dateComponents(for: calendar)
        let date = calendar.date(from: c).map { Self.day.string(from: $0) } ?? "unknown"
        let kcal = HKUnit.kilocalorie(), minutes = HKUnit.minute(), count = HKUnit.count()
        var values: [String: JSON] = [
            "active_energy_burned": QuantityCodec.encode(s.activeEnergyBurned, preferred: kcal),
            "active_energy_burned_goal": QuantityCodec.encode(s.activeEnergyBurnedGoal, preferred: kcal),
            "apple_move_time": QuantityCodec.encode(s.appleMoveTime, preferred: minutes),
            "apple_move_time_goal": QuantityCodec.encode(s.appleMoveTimeGoal, preferred: minutes),
            "apple_exercise_time": QuantityCodec.encode(s.appleExerciseTime, preferred: minutes),
            "apple_stand_hours": QuantityCodec.encode(s.appleStandHours, preferred: count),
        ]
        values.put("exercise_time_goal", s.exerciseTimeGoal.map { QuantityCodec.encode($0, preferred: minutes) })
        values.put("stand_hours_goal", s.standHoursGoal.map { QuantityCodec.encode($0, preferred: count) })
        var o: [String: JSON] = [
            "kind": "activity_summary",
            "key": .string(date),
            "date": .string(date),
            "start": JSON(calendar.date(from: c)),
            "time_zone": .string(calendar.timeZone.identifier),
            "activity_move_mode": JSON.enumValue(s.activityMoveMode.rawValue, "HKActivityMoveMode"),
            "values": .object(values),
        ]
        if #available(iOS 18.0, *) { o["paused"] = .bool(s.isPaused) }
        return .object(o)
    }

    /// Which effort-score samples rate which workout (iOS 18+), incrementally by anchor.
    private func syncEffortRelationships() async throws -> Int {
        guard #available(iOS 18.0, *) else { return 0 }
        let req = await requestedTypes()
        guard req.contains(HKWorkoutTypeIdentifier) else { return 0 }
        let ctx = try await context()
        let key = "effort-relationships"
        let previous = anchors.anchor(for: key)
        let (relationships, anchor): ([HKWorkoutEffortRelationship], HKQueryAnchor?) = try await withCheckedThrowingContinuation { cont in
            let once = ResumeOnceThrowing(cont)
            let box = QueryBox()
            let raised = store.safeExecute {
                HKWorkoutEffortRelationshipQuery(predicate: nil, anchor: previous, options: .default) { q, rel, newAnchor, error in
                    box.stop(q, store: self.store)
                    if let error { once.fail(error) } else { once.succeed((rel ?? [], newAnchor)) }
                }
            }
            if let raised { once.fail(SyncError.disallowed(raised)) }
        }
        let items = try relationships.map { r -> WireItem in
            var o: [String: JSON] = [
                "kind": "workout_effort_relationship",
                "key": .string(r.workout.uuid.uuidString + "/" + (r.activity?.uuid.uuidString ?? "workout")),
                "workout_uuid": .string(r.workout.uuid.uuidString),
                "sample_uuids": .array((r.samples ?? []).map { .string($0.uuid.uuidString) }),
                "sample_types": .array((r.samples ?? []).map { .string($0.sampleType.identifier) }),
            ]
            o.put("activity_uuid", JSON(r.activity?.uuid.uuidString))
            return try WireItem(.record, .object(o))
        }
        try await upload(items, ctx)
        if let anchor, !(previous == nil && items.isEmpty) { try anchors.save(anchor, for: key) }
        return items.count
    }

    // MARK: Helpers

    private func show(_ text: String, progress: Double? = nil) async {
        await MainActor.run {
            SyncStatus.shared.detail = text
            if let progress { SyncStatus.shared.progress = progress }
        }
    }

    /// "HKQuantityTypeIdentifierHeartRateVariabilitySDNN" -> "Heart rate variability SDNN".
    static func readableName(_ identifier: String) -> String {
        let short = shortName(identifier).replacingOccurrences(of: "TypeIdentifier", with: "")
        var words: [String] = []
        var word = ""
        let chars = Array(short)
        for (i, c) in chars.enumerated() {
            let next = i + 1 < chars.count ? chars[i + 1] : nil
            // A new word at a capital after a lower-case letter or a digit, or at the last
            // capital of a run followed by lower case ("SDNNValue" -> "SDNN", "Value").
            if c.isUppercase, !word.isEmpty,
               word.last?.isLowercase == true || word.last?.isNumber == true
               || (next?.isLowercase == true && word.last?.isUppercase == true) {
                words.append(word)
                word = ""
            }
            word.append(c)
        }
        if !word.isEmpty { words.append(word) }
        return words.enumerated().map { i, w in
            i > 0 && w.count > 1 && w.dropFirst().allSatisfy(\.isLowercase) ? w.lowercased() : w
        }.joined(separator: " ")
    }

    static func shortName(_ identifier: String) -> String {
        for p in ["HKQuantityTypeIdentifier", "HKCategoryTypeIdentifier", "HKCorrelationTypeIdentifier",
                  "HKClinicalTypeIdentifier", "HKDocumentTypeIdentifier", "HKScoredAssessmentTypeIdentifier",
                  "HKCharacteristicTypeIdentifier", "HKDataTypeIdentifier", "HKDataType", "HK"] where identifier.hasPrefix(p) && identifier.count > p.count {
            return String(identifier.dropFirst(p.count))
        }
        return identifier
    }

    @MainActor
    private static func deviceInfo() -> JSON {
        var info = utsname()
        uname(&info)
        let model = withUnsafeBytes(of: &info.machine) { buf in
            String(decoding: buf.prefix(while: { $0 != 0 }), as: UTF8.self)
        }
        return [
            "model": .string(model),
            "system_version": .string(UIDevice.current.systemVersion),
            "app_version": .string(Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? ""),
            "sdk": .string(SDKHealthTypes.sdk),
        ]
    }
}

/// Stops a long-running query from inside its own results handler.
private final class QueryBox: @unchecked Sendable {
    func stop(_ query: HKQuery, store: HKHealthStore) { store.stop(query) }
}

/// Calls an HKObserverQuery completion handler exactly once.
final class CompletionOnce: @unchecked Sendable {
    private let lock = NSLock()
    private var completion: (() -> Void)?

    init(_ completion: @escaping () -> Void) { self.completion = completion }

    var isDone: Bool {
        lock.lock()
        defer { lock.unlock() }
        return completion == nil
    }

    func call() {
        lock.lock()
        let c = completion
        completion = nil
        lock.unlock()
        c?()
    }
}

/// Asks iOS for extra time to finish an upload when the app is in the background.
@MainActor
final class BackgroundTask {
    private var id: UIBackgroundTaskIdentifier = .invalid

    /// `onExpire` runs when the extra time is over, just before iOS suspends the app: it must
    /// stop the work and answer HealthKit then, synchronously (see backgroundTimeOver).
    static func begin(_ name: String, onExpire: (@Sendable () -> Void)? = nil) -> BackgroundTask {
        let task = BackgroundTask()
        task.id = UIApplication.shared.beginBackgroundTask(withName: name) { [weak task] in
            onExpire?()
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
