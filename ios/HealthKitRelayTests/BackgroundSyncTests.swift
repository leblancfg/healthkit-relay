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

    func testCoalescerRunsOnceMoreForCallersDuringARun() async {
        let c = Coalescer()
        let runs = Counter()
        let gate = Gate()
        // The first caller starts a run that waits at the gate; five more arrive meanwhile.
        let first = Task { await c.run { await runs.add(); await gate.wait() } }
        await runs.waitFor(1)
        let others = (0..<5).map { _ in Task { await c.run { await runs.add() } } }
        try? await Task.sleep(for: .milliseconds(50))
        await gate.open()
        await first.value
        for o in others { await o.value }
        // One run for the first caller, one more for everyone who came during it.
        let n = await runs.value
        XCTAssertEqual(n, 2)
        // Idle again: the next caller gets a run of its own.
        await c.run { await runs.add() }
        let after = await runs.value
        XCTAssertEqual(after, 3)
    }

    func testCoalescerCancel() async {
        let c = Coalescer()
        XCTAssertFalse(c.cancel(), "nothing to cancel")
        let started = Counter()
        let task = Task {
            await c.run {
                await started.add()
                try? await Task.sleep(for: .seconds(30))
                XCTAssertTrue(Task.isCancelled, "the job sees the cancellation")
            }
        }
        await started.waitFor(1)
        XCTAssertTrue(c.cancel())
        await task.value
        XCTAssertFalse(c.cancel(), "the run is over")
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
