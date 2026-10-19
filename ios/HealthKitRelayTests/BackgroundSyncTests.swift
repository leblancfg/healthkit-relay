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
}
