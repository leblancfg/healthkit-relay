import Foundation
import os

/// Unified logging (Console.app, `log stream`) plus stdout, which `devicectl ... --console`
/// shows when debugging on a device.
enum Log {
    private static let logger = Logger(subsystem: Bundle.main.bundleIdentifier ?? "HealthKitRelay", category: "app")

    static func info(_ message: String) {
        logger.info("\(message, privacy: .public)")
        print(message)
    }

    static func error(_ message: String) {
        logger.error("\(message, privacy: .public)")
        print("ERROR: \(message)")
    }
}
