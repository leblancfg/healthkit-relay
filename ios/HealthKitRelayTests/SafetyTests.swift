import HealthKit
import XCTest
@testable import HealthKitRelay

/// HealthKit raises Objective-C exceptions for some types and queries; none may end the app.
/// These run against the simulator's real HealthKit, without any access granted.
final class SafetyTests: XCTestCase {
    let store = HKHealthStore()

    func testRaisingQueryFailsInsteadOfCrashing() async {
        do {
            let _: Int = try await store.safeResult { _ in
                HKObserverQuery(sampleType: NSArray().object(at: 1) as! HKSampleType, predicate: nil) { _, _, _ in }
            }
            XCTFail("expected an error")
        } catch let SyncError.disallowed(reason) {
            XCTAssertTrue(reason.contains("NSRangeException"), reason)
        } catch {
            XCTFail("\(error)")
        }

        let stream: AsyncThrowingStream<Int, Error> = store.safeStream { _ in
            HKObserverQuery(sampleType: NSArray().object(at: 1) as! HKSampleType, predicate: nil) { _, _, _ in }
        }
        do {
            for try await _ in stream { XCTFail("no values expected") }
            XCTFail("expected an error")
        } catch let SyncError.disallowed(reason) {
            XCTAssertTrue(reason.contains("NSRangeException"), reason)
        } catch {
            XCTFail("\(error)")
        }
    }

    func testTimeoutReturnsNilAndHandsOnALateAnswer() async {
        let late = expectation(description: "late answer")
        let slow: Int? = await withTimeout(0.2, late: { value in
            XCTAssertEqual(value, 7)
            late.fulfill()
        }) { done in
            DispatchQueue.global().asyncAfter(deadline: .now() + 0.6) { done(7) }
        }
        XCTAssertNil(slow)
        await fulfillment(of: [late], timeout: 3)

        let quick: Int? = await withTimeout(5) { done in done(3) }
        XCTAssertEqual(quick, 3)
    }

    /// Grant access is the main sheet only: per-object types are optional rows and never make
    /// the app ask again.
    func testGrantAccessLeavesPerObjectTypesToTheirRows() async {
        guard HKHealthStore.isHealthDataAvailable() else { return }
        let engine = SyncEngine.shared
        let plan = await engine.authPlan()
        XCTAssertTrue(plan.perObject.contains { $0.identifier == HKVisionPrescriptionTypeIdentifier })
        for type in plan.perObject {
            XCTAssertFalse(plan.read.contains(type), type.identifier)
        }

        let defaults = UserDefaults.standard
        let savedRead = defaults.stringArray(forKey: SyncEngine.askedReadKey)
        let savedPerObject = defaults.data(forKey: SyncEngine.perObjectKey)
        defer {
            defaults.set(savedRead, forKey: SyncEngine.askedReadKey)
            defaults.set(savedPerObject, forKey: SyncEngine.perObjectKey)
        }
        defaults.set(plan.read.map(\.identifier), forKey: SyncEngine.askedReadKey)
        defaults.removeObject(forKey: SyncEngine.perObjectKey)
        let needs = await engine.needsAuthorization()
        XCTAssertFalse(needs)
        let rows = await engine.perObjectRows()
        XCTAssertEqual(rows.map(\.identifier), plan.perObject.map(\.identifier))
        XCTAssertTrue(rows.allSatisfy { $0.outcome == nil })

        // A per-object type once asked is synced and observed: the observer query HealthKit
        // refuses for vision prescriptions (0.2.3 crashed there) is skipped.
        let asked = Dictionary(uniqueKeysWithValues: plan.perObject.map { ($0.identifier, PerObjectOutcome.noAnswer) })
        defaults.set(try? JSONEncoder().encode(asked), forKey: SyncEngine.perObjectKey)
        await engine.accessChanged()
        await engine.startObserving()
        let coverage = await engine.coverage()
        XCTAssertEqual(coverage.notYetRequested, 0)
    }

    func testOutcomesRoundTrip() throws {
        let all: [PerObjectOutcome] = [.answered(nil), .answered(0), .answered(3), .noAnswer, .failed("x")]
        let data = try JSONEncoder().encode(all)
        XCTAssertEqual(try JSONDecoder().decode([PerObjectOutcome].self, from: data), all)
        XCTAssertEqual(PerObjectOutcome.answered(3).text, "3 shared")
    }

    /// Observer and anchored queries and background delivery for every type the SDK has.
    func testEveryTypeQueriedWithoutCrashing() async {
        var refused: [String] = []
        var observers: [HKQuery] = []
        for synced in HealthTypes.all {
            guard let type = synced.sampleType else { continue }
            var observer: HKQuery?
            if let raised = store.safeExecute({
                let q = HKObserverQuery(sampleType: type, predicate: nil) { _, completion, _ in completion() }
                observer = q
                return q
            }) {
                refused.append("observer \(synced.key): \(raised)")
            }
            if let observer { observers.append(observer) }
            if let raised = HKRTry({ store.enableBackgroundDelivery(for: type, frequency: .hourly) { _, _ in } }) {
                refused.append("background delivery \(synced.key): \(raised)")
            }
            do {
                let _: Int = try await store.safeResult { answer in
                    HKAnchoredObjectQuery(type: type, predicate: nil, anchor: nil, limit: 1) { _, samples, _, _, error in
                        if let error { answer(.failure(error)) } else { answer(.success(samples?.count ?? 0)) }
                    }
                }
            } catch let SyncError.disallowed(reason) {
                refused.append("anchored \(synced.key): \(reason)")
            } catch {
                // Not authorized: an error, as it should be.
            }
        }
        observers.forEach(store.stop)
        _ = try? await store.disableAllBackgroundDelivery()
        print("refused by HealthKit (\(refused.count)):\n" + refused.joined(separator: "\n"))
    }
}
