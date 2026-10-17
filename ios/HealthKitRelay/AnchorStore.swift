import Foundation
import HealthKit

/// Persists one HKQueryAnchor per data type (and other small sync positions), so each sync
/// only fetches what changed. Files use complete-until-first-unlock protection, so background
/// syncs can read them.
struct AnchorStore: Sendable {
    let directory: URL

    /// Wire format v2 starts from scratch: every type, including those app 0.1 already sent,
    /// is uploaded again with full detail. The server keeps both copies apart.
    init(name: String = "anchors-v2") {
        let base = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
        directory = base.appendingPathComponent(name, isDirectory: true)
        create()
        // v1 positions are obsolete.
        try? FileManager.default.removeItem(at: base.appendingPathComponent("anchors", isDirectory: true))
    }

    private func create() {
        try? FileManager.default.createDirectory(
            at: directory, withIntermediateDirectories: true,
            attributes: [.protectionKey: FileProtectionType.completeUntilFirstUserAuthentication])
    }

    private func file(_ key: String, _ ext: String) -> URL {
        let safe = key.replacingOccurrences(of: "/", with: "_")
        return directory.appendingPathComponent(safe + "." + ext)
    }

    func anchor(for key: String) -> HKQueryAnchor? {
        guard let data = try? Data(contentsOf: file(key, "anchor")) else { return nil }
        return try? NSKeyedUnarchiver.unarchivedObject(ofClass: HKQueryAnchor.self, from: data)
    }

    func save(_ anchor: HKQueryAnchor, for key: String) throws {
        let data = try NSKeyedArchiver.archivedData(withRootObject: anchor, requiringSecureCoding: true)
        try data.write(to: file(key, "anchor"), options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
    }

    func hasAnchor(for key: String) -> Bool {
        FileManager.default.fileExists(atPath: file(key, "anchor").path)
    }

    func value(for key: String) -> String? {
        (try? String(contentsOf: file(key, "txt"), encoding: .utf8))?.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    func setValue(_ value: String, for key: String) throws {
        try Data(value.utf8).write(to: file(key, "txt"), options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
    }

    /// Forget everything: the next sync re-uploads all history (the server de-duplicates).
    func reset() {
        try? FileManager.default.removeItem(at: directory)
        create()
    }
}
