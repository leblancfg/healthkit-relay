import Foundation
import HealthKit
import UIKit

enum SyncError: LocalizedError {
    case locked
    case disallowed(String)
    case notAuthorized
    case noAnchor

    var errorDescription: String? {
        switch self {
        case .locked: "Phone is locked; will sync after the next unlock"
        case let .disallowed(reason): "HealthKit refused the query: \(reason)"
        case .notAuthorized: "Not authorized"
        case .noAnchor: "HealthKit returned no anchor"
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
}

/// Counts for the UI.
struct Coverage: Sendable {
    var sdk = SDKHealthTypes.sdk
    var inSDK = 0
    var available = 0
    var requested = 0
    var perObject = 0
    var excluded: [String: String] = [:]
    var unavailable: [String] = []
    var notYetRequested = 0
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
    private var observed = Set<String>()
    private var deliveryEnabled = Set<String>()
    private var plan: AuthPlan?
    private var requested: Set<String>?
    private var lastAux: Date?
    /// A sync was skipped because the phone was locked: run it after the next unlock.
    private(set) var pendingAfterUnlock = false

    static var isAvailable: Bool { HKHealthStore.isHealthDataAvailable() }

    init() {
        units = UnitResolver(store: store)
    }

    // MARK: Authorization

    /// Sorts every available type into one authorization request, per-object requests and
    /// exclusions. HealthKit raises an exception (not an error) for types an app may not
    /// request, so each type is probed on its own first, without showing anything.
    func authPlan() async -> AuthPlan {
        if let plan { return plan }
        var p = AuthPlan()
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
        plan = p
        return p
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
                    once.resume(error?.localizedDescription)
                }
            }
            if let raised { once.resume(raised) }
        }
    }

    func requestAuthorization() async throws {
        let p = await authPlan()
        try await withCheckedThrowingContinuation { (cont: CheckedContinuation<Void, Error>) in
            let once = ResumeOnceThrowing(cont)
            let raised = HKRTry {
                self.store.requestAuthorization(toShare: [], read: p.read) { _, error in
                    if let error { once.fail(error) } else { once.succeed(()) }
                }
            }
            if let raised { once.fail(SyncError.disallowed(raised)) }
        }
        if #available(iOS 16.0, *) {
            var asked = Set(UserDefaults.standard.stringArray(forKey: "perObjectRequested") ?? [])
            for type in p.perObject where !asked.contains(type.identifier) {
                await withCheckedContinuation { (cont: CheckedContinuation<Void, Never>) in
                    let once = ResumeOnce(cont)
                    let raised = HKRTry {
                        self.store.requestPerObjectReadAuthorization(for: type, predicate: nil) { _, _ in once.resume(()) }
                    }
                    if raised != nil { once.resume(()) }
                }
                asked.insert(type.identifier)
            }
            UserDefaults.standard.set(Array(asked), forKey: "perObjectRequested")
        }
        requested = nil
        await startObserving()
    }

    static let healthRecordsOptInKey = "healthRecordsOptIn"

    /// Whether the user chose to include Health Records.
    var healthRecordsOptedIn: Bool { UserDefaults.standard.bool(forKey: Self.healthRecordsOptInKey) }

    /// Asks for Health Records access separately (opt-in, see `AuthPlan.healthRecords`).
    func requestHealthRecords() async throws {
        let p = await authPlan()
        guard !p.healthRecords.isEmpty else { return }
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
        requested = nil
        await startObserving()
    }

    /// Whether Health Records can be offered on this device.
    func healthRecordsAvailable() async -> Bool { !(await authPlan()).healthRecords.isEmpty }

    /// Whether the authorization sheet still needs to be shown: true after an update brings
    /// new types. HealthKit never reveals which read permissions were granted.
    func needsAuthorization() async -> Bool {
        let p = await authPlan()
        if await requestStatus(p.read) != .unnecessary { return true }
        let asked = Set(UserDefaults.standard.stringArray(forKey: "perObjectRequested") ?? [])
        return p.perObject.contains { !asked.contains($0.identifier) }
    }

    /// Types the user has been asked about. Only those sync: an anchored query over a type
    /// never requested returns nothing, and its anchor must not advance past real history.
    private func requestedTypes() async -> Set<String> {
        if let requested { return requested }
        let p = await authPlan()
        var out = Set<String>()
        for type in p.read {
            let set = Set([type] + (p.companions[type.identifier] ?? []))
            if await requestStatus(set) == .unnecessary {
                out.insert(type.identifier)
            }
        }
        let asked = Set(UserDefaults.standard.stringArray(forKey: "perObjectRequested") ?? [])
        for type in p.perObject where asked.contains(type.identifier) { out.insert(type.identifier) }
        if healthRecordsOptedIn {
            for type in p.healthRecords where await requestStatus([type]) == .unnecessary {
                out.insert(type.identifier)
            }
        }
        if !out.isEmpty {
            for type in p.derived { out.insert(type.identifier) }
        }
        requested = out
        return out
    }

    func coverage() async -> Coverage {
        let p = await authPlan()
        let req = await requestedTypes()
        let catalog = HealthTypes.catalog
        return Coverage(
            inSDK: SDKHealthTypes.identified.count + SDKHealthTypes.factories.count,
            available: catalog.types.count,
            requested: req.count,
            perObject: p.perObject.count,
            excluded: p.excluded,
            unavailable: catalog.unavailable,
            notYetRequested: max(0, p.read.count + p.perObject.count + p.derived.count
                + (healthRecordsOptedIn ? p.healthRecords.count : 0) - req.count))
    }

    // MARK: Background delivery

    /// Registers an observer query per sample type and asks HealthKit to wake the app (at most
    /// hourly) when new data arrives. Must run at every launch, including background ones.
    func startObserving() async {
        guard Self.isAvailable else { return }
        let req = await requestedTypes()
        for synced in HealthTypes.all {
            guard let type = synced.sampleType, req.contains(synced.key) else { continue }
            let key = synced.key
            if !observed.contains(key) {
                let query = HKObserverQuery(sampleType: type, predicate: nil) { _, completion, error in
                    let done = CompletionOnce(completion)
                    guard error == nil else { done.call(); return }
                    Task {
                        let task = await BackgroundTask.begin("sync \(key)")
                        await SyncEngine.shared.observerFired(key, done: done)
                        await task.end()
                    }
                }
                if HKRTry({ self.store.execute(query) }) == nil { observed.insert(key) }
            }
            if !deliveryEnabled.contains(key) {
                let ok = await withCheckedContinuation { (cont: CheckedContinuation<Bool, Never>) in
                    let once = ResumeOnce(cont)
                    let raised = HKRTry {
                        self.store.enableBackgroundDelivery(for: type, frequency: .hourly) { ok, _ in once.resume(ok) }
                    }
                    if raised != nil { once.resume(false) }
                }
                if ok { deliveryEnabled.insert(key) }
            }
        }
    }

    /// HealthKit stops waking an app that does not call the observer completion promptly, so it
    /// is called when the type is synced or after 20 s, whichever comes first; the sync goes on
    /// for as long as iOS lets the app run.
    private func observerFired(_ key: String, done: CompletionOnce) async {
        Task {
            try? await Task.sleep(for: .seconds(20))
            done.call()
        }
        if let synced = HealthTypes.named(key) {
            _ = try? await sync(synced)
        }
        done.call()
        _ = try? await syncAuxIfDue()
    }

    // MARK: Sync

    /// Syncs everything. Returns the number of records uploaded.
    @discardableResult
    func syncAll() async -> Int {
        await MainActor.run {
            SyncStatus.shared.running = true
            SyncStatus.shared.lastError = nil
        }
        var total = 0
        var firstError: Error?
        do {
            total += try await syncAux()
        } catch {
            firstError = error
        }
        let req = await requestedTypes()
        let types = HealthTypes.all.filter { $0.sampleType != nil && req.contains($0.key) }
        for (i, synced) in types.enumerated() where !Self.isFatal(firstError) {
            await show("\(i + 1)/\(types.count) \(Self.shortName(synced.key))")
            do {
                total += try await sync(synced)
            } catch SyncError.notAuthorized, SyncError.disallowed {
                continue  // read through other types, or access not granted: nothing to report
            } catch {
                firstError = firstError ?? error
            }
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

    /// Locked phone, missing settings or an outdated server affect every type: stop there.
    private static func isFatal(_ error: Error?) -> Bool {
        guard let error else { return false }
        if let e = error as? SyncError, case .locked = e { return true }
        if error is CancellationError { return true }
        if let e = error as? UploadError {
            switch e {
            case .notConfigured, .serverTooOld: return true
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
            return try await body()
        }
        inFlight[key] = task
        defer { inFlight[key] = nil }
        return try await task.value
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

    func syncAfterUnlockIfPending() async {
        guard pendingAfterUnlock else { return }
        pendingAfterUnlock = false
        await syncAll()
    }

    /// Forgets all sync positions, so the next sync re-sends all history.
    func resetAnchors() async {
        anchors.reset()
    }

    private struct Context {
        let uploader: Uploader
        let device: JSON
    }

    private func context() async throws -> Context {
        let settings = ServerSettings.load()
        guard settings.isComplete else { throw UploadError.notConfigured }
        guard await MainActor.run(body: { UIApplication.shared.isProtectedDataAvailable }) else {
            pendingAfterUnlock = true
            throw SyncError.locked
        }
        return Context(uploader: Uploader(settings: settings), device: await Self.deviceInfo())
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
            await show("\(Self.shortName(key)): \(shown) uploaded")
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
                let query = HKAnchoredObjectQuery(type: type, predicate: nil, anchor: anchor, limit: limit) {
                    _, samples, deleted, newAnchor, error in
                    if let error {
                        once.fail(error)
                    } else if let newAnchor {
                        once.succeed(Page(samples: samples ?? [], deleted: deleted ?? [], anchor: newAnchor))
                    } else {
                        once.fail(SyncError.noAnchor)
                    }
                }
                if let raised = HKRTry({ self.store.execute(query) }) {
                    once.fail(SyncError.disallowed(raised))
                }
            }
        } catch let error as HKError where error.code == .errorDatabaseInaccessible {
            pendingAfterUnlock = true
            throw SyncError.locked
        } catch let error as HKError where error.code == .errorAuthorizationDenied || error.code == .errorAuthorizationNotDetermined {
            throw SyncError.notAuthorized
        }
    }

    /// Posts items in order, cut into batches by size. Records come before the sample they
    /// belong to, so an archived sample always has its series and files.
    private func upload(_ items: [WireItem], _ ctx: Context) async throws {
        var batch = WireBatch(device: ctx.device, items: [])
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
            do { values[name] = try body() } catch { errors[name] = .string(error.localizedDescription) }
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
        let meds = (try? await HKUserAnnotatedMedicationQueryDescriptor().result(for: store)) ?? []
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
            let predicate = HKQuery.predicate(forActivitySummariesBetweenStart: from, end: to)
            let summaries = try await HKActivitySummaryQueryDescriptor(predicate: predicate).result(for: store)
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
            let query = HKWorkoutEffortRelationshipQuery(predicate: nil, anchor: previous, options: .default) { q, rel, newAnchor, error in
                box.stop(q, store: self.store)
                if let error { once.fail(error) } else { once.succeed((rel ?? [], newAnchor)) }
            }
            if let raised = HKRTry({ self.store.execute(query) }) { once.fail(SyncError.disallowed(raised)) }
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

    private func show(_ text: String) async {
        await MainActor.run { SyncStatus.shared.detail = text }
    }

    static func shortName(_ identifier: String) -> String {
        for p in ["HKQuantityTypeIdentifier", "HKCategoryTypeIdentifier", "HKCorrelationTypeIdentifier",
                  "HKClinicalTypeIdentifier", "HKDocumentTypeIdentifier", "HKScoredAssessmentTypeIdentifier",
                  "HKDataTypeIdentifier", "HK"] where identifier.hasPrefix(p) && identifier.count > p.count {
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
