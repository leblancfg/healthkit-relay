import Foundation
import os

/// Unified logging (Console.app, `log stream`), stdout (shown by `devicectl ... --console`)
/// and a small file in Application Support/Logs (shared from Advanced, or copied off a
/// development device with `devicectl device copy from --domain-type appDataContainer`).
/// Messages name types and counts, never health values or tokens.
enum Log {
    private static let logger = Logger(subsystem: Bundle.main.bundleIdentifier ?? "HealthKitRelay", category: "app")
    private static let queue = DispatchQueue(label: "log-file")
    private static let maxBytes = 1 << 20

    static let fileURL: URL = {
        let dir = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("Logs", isDirectory: true)
        try? FileManager.default.createDirectory(
            at: dir, withIntermediateDirectories: true,
            attributes: [.protectionKey: FileProtectionType.completeUntilFirstUserAuthentication])
        return dir.appendingPathComponent("HealthRelay.log")
    }()

    static func info(_ message: String) {
        logger.info("\(message, privacy: .public)")
        print(message)
        write(message)
    }

    static func error(_ message: String) {
        logger.error("\(message, privacy: .public)")
        print("ERROR: \(message)")
        write("ERROR: " + message)
    }

    private static func write(_ message: String) {
        let line = Date.now.formatted(.iso8601.year().month().day().time(includingFractionalSeconds: true)) + " " + message + "\n"
        queue.async {
            let url = fileURL
            let fm = FileManager.default
            if let size = try? fm.attributesOfItem(atPath: url.path)[.size] as? Int, size > maxBytes {
                let old = url.appendingPathExtension("1")
                try? fm.removeItem(at: old)
                try? fm.moveItem(at: url, to: old)
            }
            if !fm.fileExists(atPath: url.path) {
                fm.createFile(atPath: url.path, contents: nil, attributes: [.protectionKey: FileProtectionType.completeUntilFirstUserAuthentication])
            }
            guard let h = try? FileHandle(forWritingTo: url) else { return }
            defer { try? h.close() }
            _ = try? h.seekToEnd()
            try? h.write(contentsOf: Data(line.utf8))
        }
    }
}
