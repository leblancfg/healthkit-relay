import Foundation

/// Resumes a continuation exactly once, for HealthKit callbacks that may fire several times
/// or not at all (when the call raised).
final class ResumeOnce<T>: @unchecked Sendable {
    private let lock = NSLock()
    private var continuation: CheckedContinuation<T, Never>?

    init(_ continuation: CheckedContinuation<T, Never>) { self.continuation = continuation }

    /// Returns whether this call was the one that resumed.
    @discardableResult
    func resume(_ value: T) -> Bool {
        lock.lock()
        let c = continuation
        continuation = nil
        lock.unlock()
        c?.resume(returning: value)
        return c != nil
    }
}

/// The throwing variant of ResumeOnce.
final class ResumeOnceThrowing<T>: @unchecked Sendable {
    private let lock = NSLock()
    private var continuation: CheckedContinuation<T, Error>?

    init(_ continuation: CheckedContinuation<T, Error>) { self.continuation = continuation }

    private func take() -> CheckedContinuation<T, Error>? {
        lock.lock()
        defer { lock.unlock() }
        let c = continuation
        continuation = nil
        return c
    }

    func succeed(_ value: T) { take()?.resume(returning: value) }
    func fail(_ error: Error) { take()?.resume(throwing: error) }
}
