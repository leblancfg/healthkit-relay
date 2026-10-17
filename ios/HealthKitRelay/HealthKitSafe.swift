import Foundation
import HealthKit

// HealthKit reports some misuse with Objective-C exceptions instead of errors: an observer
// query for vision prescriptions, a type an app may not request, an activity-summary predicate
// without a calendar... Swift cannot catch those, so one would end the app. Every query here is
// therefore created and executed inside HKRTry, through these helpers; a query HealthKit
// refuses fails with SyncError.disallowed instead. The async query descriptors are not used:
// they create their queries where HKRTry cannot reach.

extension HKHealthStore {
    /// Builds and executes a query. Returns the reason HealthKit raised, or nil.
    func safeExecute(_ build: () -> HKQuery) -> String? {
        HKRTry { self.execute(build()) }
    }

    /// A query that answers once. `build` gets the function to answer with; later answers
    /// are ignored.
    func safeResult<T>(_ build: (@escaping @Sendable (Result<T, Error>) -> Void) -> HKQuery) async throws -> T {
        try await withCheckedThrowingContinuation { (cont: CheckedContinuation<T, Error>) in
            let once = ResumeOnceThrowing(cont)
            let raised = safeExecute {
                build { result in
                    switch result {
                    case let .success(value): once.succeed(value)
                    case let .failure(error): once.fail(error)
                    }
                }
            }
            if let raised { once.fail(SyncError.disallowed(raised)) }
        }
    }

    /// A query whose handler delivers many values (series points), as a stream. The query is
    /// stopped when the stream ends or its reader stops early.
    func safeStream<T>(_ build: (AsyncThrowingStream<T, Error>.Continuation) -> HKQuery) -> AsyncThrowingStream<T, Error> {
        AsyncThrowingStream { cont in
            var query: HKQuery?
            if let raised = HKRTry({
                let q = build(cont)
                query = q
                self.execute(q)
            }) {
                cont.finish(throwing: SyncError.disallowed(raised))
                return
            }
            if let query {
                cont.onTermination = { [weak self] _ in self?.stop(query) }
            }
        }
    }

    /// Every sample of a type matching the predicate (built inside HKRTry too).
    func safeSamples(_ type: HKSampleType, limit: Int = HKObjectQueryNoLimit,
                     predicate: () -> NSPredicate? = { nil }) async throws -> [HKSample] {
        try await safeResult { answer in
            HKSampleQuery(sampleType: type, predicate: predicate(), limit: limit, sortDescriptors: nil) { _, samples, error in
                if let error { answer(.failure(error)) } else { answer(.success(samples ?? [])) }
            }
        }
    }

    /// The preferred unit of a quantity type, nil if HealthKit has none or refuses.
    func safePreferredUnit(_ type: HKQuantityType) async -> HKUnit? {
        await withCheckedContinuation { (cont: CheckedContinuation<HKUnit?, Never>) in
            let once = ResumeOnce(cont)
            let raised = HKRTry {
                self.preferredUnits(for: [type]) { units, error in once.resume(error == nil ? units[type] : nil) }
            }
            if raised != nil { once.resume(nil) }
        }
    }
}

/// Waits for `body` to call back, at most `seconds`. Returns nil on timeout; a late answer is
/// passed to `late` instead (HealthKit sheets can outlive the wait).
func withTimeout<T: Sendable>(_ seconds: Double, late: (@Sendable (T) -> Void)? = nil,
                              _ body: (@escaping @Sendable (T) -> Void) -> Void) async -> T? {
    await withCheckedContinuation { (cont: CheckedContinuation<T?, Never>) in
        let once = ResumeOnce(cont)
        body { value in
            if !once.resume(value) { late?(value) }
        }
        DispatchQueue.global().asyncAfter(deadline: .now() + seconds) { once.resume(nil) }
    }
}
