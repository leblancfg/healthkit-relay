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
        raw(key, "anchor").flatMap(Self.unarchive)
    }

    static func unarchive(_ data: Data) -> HKQueryAnchor? {
        try? NSKeyedUnarchiver.unarchivedObject(ofClass: HKQueryAnchor.self, from: data)
    }

    static func archive(_ anchor: HKQueryAnchor) throws -> Data {
        try NSKeyedArchiver.archivedData(withRootObject: anchor, requiringSecureCoding: true)
    }

    func save(_ anchor: HKQueryAnchor, for key: String) throws {
        try write(Self.archive(anchor), key, "anchor")
    }

    /// A position's file as stored, nil when there is none.
    func raw(_ key: String, _ ext: String) -> Data? {
        try? Data(contentsOf: file(key, ext))
    }

    private func write(_ data: Data, _ key: String, _ ext: String) throws {
        try data.write(to: file(key, ext), options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
    }

    /// Moving `key` from the anchor it had when the page was read (`previous`, its file) to
    /// `anchor`, once the server confirmed the page.
    func commit(_ anchor: HKQueryAnchor, for key: String, from previous: Data?) throws -> UploadCommit {
        UploadCommit(key: key, ext: "anchor", from: previous, to: try Self.archive(anchor))
    }

    /// Moving the position `key` (a value, see setValue) from what it is now to `value`.
    func commit(value: String, for key: String) -> UploadCommit {
        UploadCommit(key: key, ext: "txt", from: raw(key, "txt"), to: Data(value.utf8))
    }

    /// Applies `commit` if the position is still the one it started from; returns whether it
    /// did. An upload that finished after its sync was cut short (see BackgroundUploads) moves
    /// a position only if no later sync moved it meanwhile: positions never go back past data
    /// the server confirmed, and never forward past data it did not.
    @discardableResult
    func apply(_ commit: UploadCommit) throws -> Bool {
        guard raw(commit.key, commit.ext) == commit.from else { return false }
        try write(commit.to, commit.key, commit.ext)
        return true
    }

    func hasAnchor(for key: String) -> Bool {
        FileManager.default.fileExists(atPath: file(key, "anchor").path)
    }

    func value(for key: String) -> String? {
        (try? String(contentsOf: file(key, "txt"), encoding: .utf8))?.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    func setValue(_ value: String, for key: String) throws {
        try write(Data(value.utf8), key, "txt")
    }

    /// No position saved yet.
    var isEmpty: Bool {
        ((try? FileManager.default.contentsOfDirectory(atPath: directory.path)) ?? []).isEmpty
    }

    /// Forget everything: the next sync re-uploads all history (the server de-duplicates).
    func reset() {
        try? FileManager.default.removeItem(at: directory)
        create()
    }
}

/// A sync position to move once an upload is confirmed: `key`'s file goes from `from` (nil:
/// none yet) to `to`. Saved with an upload that may finish in a later process.
struct UploadCommit: Codable, Equatable, Sendable {
    var key: String
    /// "anchor" (an HKQueryAnchor) or "txt" (a value).
    var ext: String
    var from: Data?
    var to: Data
}
