import XCTest
@testable import HealthKitRelay

final class BackgroundSyncTests: XCTestCase {
    private var defaults: UserDefaults!
    private let suite = "BackgroundSyncTests"

    override func setUp() {
        defaults = UserDefaults(suiteName: suite)
        defaults.removePersistentDomain(forName: suite)
    }

    override func tearDown() {
        defaults.removePersistentDomain(forName: suite)
    }

    func testPendingSyncPersists() {
        XCTAssertNil(PendingSync.load(defaults))
        PendingSync.save(.locked, defaults)
        // A new process reads it back.
        XCTAssertEqual(PendingSync.load(UserDefaults(suiteName: suite)!), .locked)
        PendingSync.save(nil, defaults)
        XCTAssertNil(PendingSync.load(defaults))
    }

    func testPendingSyncMerge() {
        XCTAssertEqual(PendingSync.merge(nil, .failed), .failed)
        XCTAssertEqual(PendingSync.merge(.failed, .locked), .locked)
        XCTAssertEqual(PendingSync.merge(.locked, .failed), .locked)
    }

    func testPendingSyncTrigger() {
        XCTAssertEqual(PendingSync.locked.trigger(.unlock), .unlock)
        XCTAssertEqual(PendingSync.locked.trigger(.refresh), .refresh)
        XCTAssertEqual(PendingSync.failed.trigger(.unlock), .retry)
        XCTAssertEqual(PendingSync.failed.trigger(.refresh), .retry)
    }

    func testObservedTypesUnknownMeansAllSampleTypes() {
        let all = HealthTypes.all
        let launch = ObservedTypes.atLaunch(defaults, all: all)
        XCTAssertFalse(launch.isEmpty)
        XCTAssertEqual(launch.count, all.filter { $0.sampleType != nil }.count)
        XCTAssertTrue(launch.allSatisfy { $0.sampleType != nil })
    }

    func testObservedTypesSavedAtRequest() {
        let steps = "HKQuantityTypeIdentifierStepCount", heart = "HKQuantityTypeIdentifierHeartRate"
        ObservedTypes.add([steps], defaults)
        ObservedTypes.add([heart, steps], defaults)
        XCTAssertEqual(ObservedTypes.load(defaults), [steps, heart])
        // Characteristics are not sample types: never observed.
        ObservedTypes.add(["HKCharacteristicTypeIdentifierDateOfBirth"], defaults)
        XCTAssertEqual(Set(ObservedTypes.atLaunch(defaults).map(\.key)), [steps, heart])
    }

    func testObservedTypesNeverSavedEmpty() {
        ObservedTypes.add([], defaults)
        XCTAssertNil(ObservedTypes.load(defaults), "an empty list would observe nothing")
    }

    func testTriggerIsTaskLocal() async {
        XCTAssertEqual(SyncTrigger.current, .foreground)
        let inner = await SyncTrigger.$current.withValue(.observer) {
            // Tasks a sync starts (SyncEngine.exclusive) inherit it.
            await Task { SyncTrigger.current }.value
        }
        XCTAssertEqual(inner, .observer)
        XCTAssertEqual(SyncTrigger.current, .foreground)
    }

    // MARK: Observer wakes

    /// A pass that reads `keys` in order, like SyncEngine.runAll; `during(key)` runs while a
    /// type is being read.
    private func reader(_ s: SyncSessions, keys: [String], log: Passes,
                        during: @escaping @Sendable (Int, String) async -> Void = { _, _ in }) -> SyncSessions.Run {
        { pass in
            let n = log.add(pass)
            for key in keys where pass.only?.contains(key) ?? true {
                s.willSync(key)
                await during(n, key)
                s.didSync(key)
            }
            return .init(uploaded: 1, failed: false)
        }
    }

    func testBurstOfWakesIsOnePass() async {
        let active = Flags()
        let s = SyncSessions(debounce: .milliseconds(200)) { active.add($0) }
        let log = Passes(), ends = Ends()
        let keys = (0..<50).map { "type\($0)" }
        let run = reader(s, keys: keys, log: log)
        let dones = keys.map { _ in Counter.Sync() }
        // Fifty wakes, spread over 100 ms, all inside the wait for the rest of the burst.
        await withTaskGroup(of: Void.self) { group in
            for (i, key) in keys.enumerated() {
                group.addTask {
                    try? await Task.sleep(for: .milliseconds(i * 2))
                    await s.request(.observer, allAux: false, key: key, done: CompletionOnce { dones[i].add() },
                                    run: run, ended: { ends.add($0) })
                }
            }
        }
        XCTAssertEqual(log.all.count, 1, "one pass for the burst")
        XCTAssertEqual(log.all.first, .init(trigger: .observer, allAux: false, only: nil))
        XCTAssertTrue(dones.allSatisfy { $0.value == 1 }, "every wake answered once")
        XCTAssertEqual(ends.all.map(\.passes), [1], "one session, one heartbeat")
        XCTAssertEqual(ends.all.first?.triggers, [.observer])
        XCTAssertFalse(s.isActive)
        await active.settle()
        XCTAssertEqual(active.all, [true, false], "one sync on screen")
    }

    func testOtherTriggersJoinAndStartAtOnce() async {
        let s = SyncSessions(debounce: .seconds(30))
        let log = Passes(), ends = Ends()
        let run = reader(s, keys: ["a", "b"], log: log)
        let started = Date.now
        async let wake: Void = s.request(.observer, allAux: false, key: "a", done: CompletionOnce {}, run: run, ended: { ends.add($0) })
        try? await Task.sleep(for: .milliseconds(50))
        // The unlock does not wait for more wakes.
        async let unlock: Void = s.request(.unlock, allAux: true, run: run, ended: { ends.add($0) })
        async let open: Void = s.request(.foreground, allAux: true, run: run, ended: { ends.add($0) })
        _ = await (wake, unlock, open)
        XCTAssertLessThan(Date.now.timeIntervalSince(started), 5)
        XCTAssertEqual(log.all, [.init(trigger: .observer, allAux: true, only: nil)], "one pass, tagged with the first trigger")
        XCTAssertEqual(ends.all.count, 1)
        XCTAssertEqual(ends.all.first?.triggers.first, .observer)
        XCTAssertEqual(Set(ends.all.first?.triggers ?? []), [.observer, .unlock, .foreground])
    }

    func testWakeForATypeNotReadYetJoinsThePass() async {
        let s = SyncSessions(debounce: .milliseconds(10))
        let log = Passes(), ends = Ends()
        let gate = Gate()
        let late = Counter.Sync()
        let run = reader(s, keys: ["a", "b"], log: log) { _, key in if key == "a" { await gate.wait() } }
        async let first: Void = s.request(.foreground, allAux: true, run: run, ended: { ends.add($0) })
        while log.all.isEmpty { try? await Task.sleep(for: .milliseconds(5)) }
        // While "a" is read, "b" has new data: the pass reads "b" after.
        async let wake: Void = s.request(.observer, allAux: false, key: "b", done: CompletionOnce { late.add() },
                                         run: run, ended: { ends.add($0) })
        try? await Task.sleep(for: .milliseconds(50))
        await gate.open()
        _ = await (first, wake)
        XCTAssertEqual(log.all.count, 1)
        XCTAssertEqual(late.value, 1)
        XCTAssertEqual(ends.all.count, 1)
    }

    func testWakeForATypeAlreadyReadIsNotLost() async {
        let s = SyncSessions(debounce: .milliseconds(10))
        let log = Passes(), ends = Ends()
        let gate = Gate()
        let answeredAfter = Flags()
        let run = reader(s, keys: ["a", "b"], log: log) { n, key in if n == 1 && key == "b" { await gate.wait() } }
        async let first: Void = s.request(.observer, allAux: false, key: "a", done: CompletionOnce {}, run: run, ended: { ends.add($0) })
        while log.all.isEmpty { try? await Task.sleep(for: .milliseconds(5)) }
        try? await Task.sleep(for: .milliseconds(20))
        // "a" was read already when its new data comes: one follow-up pass, for "a" only.
        async let wake: Void = s.request(.observer, allAux: false, key: "a",
                                         done: CompletionOnce { answeredAfter.add(log.all.count == 2) },
                                         run: run, ended: { ends.add($0) })
        try? await Task.sleep(for: .milliseconds(50))
        await gate.open()
        _ = await (first, wake)
        XCTAssertEqual(log.all.map(\.only), [nil, ["a"]])
        XCTAssertEqual(answeredAfter.all, [true], "answered once the follow-up read it")
        XCTAssertEqual(ends.all.map(\.passes), [2], "still one session")
    }

    func testWakesDuringTheFollowUpStartANewSession() async {
        let s = SyncSessions(debounce: .milliseconds(10))
        let log = Passes(), ends = Ends()
        let answered = Counter.Sync()
        // The first pass and its follow-up each get new data for "a" while reading it.
        let detached = reader(s, keys: ["a"], log: log) { n, _ in
            guard n <= 2 else { return }
            Task { await s.request(.observer, allAux: false, key: "a", done: CompletionOnce { answered.add() },
                                   run: { _ in .init(uploaded: 0, failed: false) }, ended: { _ in }) }
            try? await Task.sleep(for: .milliseconds(30))
        }
        await s.request(.foreground, allAux: true, run: detached, ended: { ends.add($0) })
        while s.isActive { try? await Task.sleep(for: .milliseconds(10)) }
        XCTAssertEqual(log.all.map(\.only), [nil, ["a"], nil], "a pass, one follow-up, then a new session")
        XCTAssertEqual(answered.value, 2, "both wakes answered")
        XCTAssertEqual(ends.all.count, 2)
    }

    func testFailedPassAnswersWakesAndRunsAgainOnlyIfSomethingJoined() async {
        let s = SyncSessions(debounce: .milliseconds(10))
        let log = Passes(), ends = Ends()
        let answered = Counter.Sync()
        let failing: SyncSessions.Run = { pass in
            log.add(pass)
            return .init(uploaded: 0, failed: true)  // locked: nothing read
        }
        await s.request(.observer, allAux: false, key: "a", done: CompletionOnce { answered.add() }, run: failing, ended: { ends.add($0) })
        XCTAssertEqual(log.all.count, 1, "no retry of its own")
        XCTAssertEqual(answered.value, 1)
        // An unlock while a pass fails: once more.
        let gate = Gate()
        let slow: SyncSessions.Run = { pass in
            let n = log.add(pass)
            if n == 2 { await gate.wait() }
            return .init(uploaded: 0, failed: n == 2)
        }
        async let wake: Void = s.request(.observer, allAux: false, key: "a", done: CompletionOnce { answered.add() }, run: slow, ended: { ends.add($0) })
        while log.all.count < 2 { try? await Task.sleep(for: .milliseconds(5)) }
        async let unlock: Void = s.request(.unlock, allAux: true, run: slow, ended: { ends.add($0) })
        try? await Task.sleep(for: .milliseconds(50))
        await gate.open()
        _ = await (wake, unlock)
        XCTAssertEqual(log.all.count, 3)
        XCTAssertEqual(answered.value, 2)
    }

    func testCancelAnswersWakesAndEnds() async {
        let s = SyncSessions(debounce: .milliseconds(10))
        XCTAssertFalse(s.cancel(), "nothing to cancel")
        let log = Passes(), ends = Ends()
        let answered = Counter.Sync()
        let run: SyncSessions.Run = { pass in
            log.add(pass)
            try? await Task.sleep(for: .seconds(30))
            XCTAssertTrue(Task.isCancelled, "the pass sees the cancellation")
            return .init(uploaded: 0, failed: true)
        }
        async let wake: Void = s.request(.observer, allAux: false, key: "a", done: CompletionOnce { answered.add() }, run: run, ended: { ends.add($0) })
        while log.all.isEmpty { try? await Task.sleep(for: .milliseconds(5)) }
        XCTAssertTrue(s.cancel())
        await wake
        XCTAssertEqual(answered.value, 1)
        XCTAssertEqual(log.all.count, 1, "no follow-up after a cancel")
        XCTAssertFalse(s.cancel(), "the session is over")
    }

    func testAnswerAllAnswersOpenObserverCalls() {
        let observers = Observers()
        let answered = Counter.Sync()
        let a = CompletionOnce { answered.add() }, b = CompletionOnce { answered.add() }
        observers.wait(a)
        observers.wait(b)
        a.call()
        XCTAssertEqual(observers.answerAll(), 1, "only b was still open")
        XCTAssertEqual(answered.value, 2)
        XCTAssertTrue(b.isDone)
        b.call()
        XCTAssertEqual(answered.value, 2, "answered once")
        XCTAssertEqual(observers.answerAll(), 0)
    }

    func testDeliveryFrequency() {
        XCTAssertEqual(Observers.frequency("HKQuantityTypeIdentifierHeartRate"), .immediate)
        XCTAssertEqual(Observers.frequency("HKCategoryTypeIdentifierSleepAnalysis"), .immediate)
        XCTAssertEqual(Observers.frequency("HKQuantityTypeIdentifierBodyMass"), .hourly)
        // Every immediate type is one the app reads.
        let known = Set(HealthTypes.all.compactMap { $0.sampleType == nil ? nil : $0.key })
        XCTAssertTrue(Observers.immediate.isSubset(of: known))
    }

    func testHeartbeatWireFormat() throws {
        let beat = AppHeartbeat(trigger: "refresh", appState: "background", protectedData: false, outcome: "locked",
                                error: nil, lastError: "heartbeat not sent: offline", pending: "locked", uploaded: 0,
                                seconds: 0.2, backgroundTimeLeft: 27, processAge: 5400, appVersion: "0.6.2 (9)",
                                wakes: ["refresh": WakeCount(count: 3, last: Date(timeIntervalSince1970: 1_792_500_000))])
        let json = try XCTUnwrap(JSONSerialization.jsonObject(with: AppHeartbeat.encoder.encode(beat)) as? [String: Any])
        // The names the server reads (server/heartbeat.go).
        XCTAssertEqual(Set(json.keys), ["trigger", "app_state", "protected_data", "outcome", "last_error", "pending",
                                        "uploaded", "seconds", "background_time_left", "process_age", "app_version", "wakes"])
        let wake = try XCTUnwrap((json["wakes"] as? [String: Any])?["refresh"] as? [String: Any])
        XCTAssertEqual(wake["count"] as? Int, 3)
        XCTAssertEqual(wake["last"] as? String, "2026-10-20T12:40:00Z")
    }

    func testOutcome() {
        XCTAssertEqual(SyncEngine.outcome(nil), "ok")
        XCTAssertEqual(SyncEngine.outcome(CancellationError()), "cancelled")
        XCTAssertEqual(SyncEngine.outcome(SyncError.locked), "locked")
        XCTAssertEqual(SyncEngine.outcome(UploadError.failed("offline")), "upload failed")
        XCTAssertEqual(SyncEngine.outcome(SyncError.noAccess), "failed")
    }
}

/// Counts, and lets a test wait for a count.
private actor Counter {
    private(set) var value = 0
    func add() { value += 1 }
    func waitFor(_ n: Int) async {
        while value < n { try? await Task.sleep(for: .milliseconds(5)) }
    }

    final class Sync: @unchecked Sendable {
        private let lock = NSLock()
        private var n = 0
        func add() { lock.withLock { n += 1 } }
        var value: Int { lock.withLock { n } }
    }
}

/// Holds jobs until opened.
private actor Gate {
    private var isOpen = false
    func open() { isOpen = true }
    func wait() async {
        while !isOpen { try? await Task.sleep(for: .milliseconds(5)) }
    }
}

/// Passes a test's SyncSessions ran.
private final class Passes: @unchecked Sendable {
    private let lock = NSLock()
    private var passes: [SyncSessions.Pass] = []
    /// Returns the pass's number, from 1.
    @discardableResult
    func add(_ p: SyncSessions.Pass) -> Int { lock.withLock { passes.append(p); return passes.count } }
    var all: [SyncSessions.Pass] { lock.withLock { passes } }
}

private final class Ends: @unchecked Sendable {
    private let lock = NSLock()
    private var ends: [SyncSessions.Summary] = []
    func add(_ s: SyncSessions.Summary) { lock.withLock { ends.append(s) } }
    var all: [SyncSessions.Summary] { lock.withLock { ends } }
}

private final class Flags: @unchecked Sendable {
    private let lock = NSLock()
    private var flags: [Bool] = []
    func add(_ f: Bool) { lock.withLock { flags.append(f) } }
    var all: [Bool] { lock.withLock { flags } }
    func settle() async { try? await Task.sleep(for: .milliseconds(20)) }
}
