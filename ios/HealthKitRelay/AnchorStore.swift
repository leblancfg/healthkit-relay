import Foundation
import HealthKit

/// Persists one HKQueryAnchor per data type, so each sync only fetches what changed.
/// Files use complete-until-first-unlock protection, so background syncs can read them.
struct AnchorStore: Sendable {
    let directory: URL

    init() {
        let base = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
        directory = base.appendingPathComponent("anchors", isDirectory: true)
        try? FileManager.default.createDirectory(
            at: directory, withIntermediateDirectories: true,
            attributes: [.protectionKey: FileProtectionType.completeUntilFirstUserAuthentication])
    }

    private func file(_ key: String) -> URL {
        directory.appendingPathComponent(key + ".anchor")
    }

    func anchor(for key: String) -> HKQueryAnchor? {
        guard let data = try? Data(contentsOf: file(key)) else { return nil }
        return try? NSKeyedUnarchiver.unarchivedObject(ofClass: HKQueryAnchor.self, from: data)
    }

    func save(_ anchor: HKQueryAnchor, for key: String) throws {
        let data = try NSKeyedArchiver.archivedData(withRootObject: anchor, requiringSecureCoding: true)
        try data.write(to: file(key), options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
    }

    /// Forget all anchors: the next sync re-uploads everything (the server de-duplicates).
    func reset() {
        try? FileManager.default.removeItem(at: directory)
        try? FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
    }
}
