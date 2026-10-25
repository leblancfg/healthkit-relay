import HealthKit
import XCTest
@testable import HealthKitRelay

final class SettingsTests: XCTestCase {
    func testEndpoint() {
        XCTAssertEqual(ServerSettings(url: "https://h.example.com", token: "t").endpoint?.absoluteString,
                       "https://h.example.com/v1/samples")
        XCTAssertEqual(ServerSettings(url: " https://h.example.com/relay/ ", token: "t").endpoint?.absoluteString,
                       "https://h.example.com/relay/v1/samples")
        XCTAssertNil(ServerSettings(url: "h.example.com", token: "t").endpoint)
        XCTAssertNil(ServerSettings(url: "ftp://h.example.com", token: "t").endpoint)
        XCTAssertFalse(ServerSettings(url: "https://h.example.com", token: "").isComplete)
    }

    func testTimestamp() {
        let date = Date(timeIntervalSince1970: 1_767_323_045.5)
        XCTAssertEqual(WireEncoder.timestamp(date), "2026-01-02T03:04:05.500Z")
    }

    func testReinstallStartsUnpaired() throws {
        let saved = ServerSettings.load()
        defer { if saved.url.isEmpty { ServerSettings.clear() } else { try? saved.save() } }
        let defaults = try XCTUnwrap(UserDefaults(suiteName: "SettingsTests.reinstall"))
        defaults.removePersistentDomain(forName: "SettingsTests.reinstall")

        // Keychain settings with no app state: left by a deleted install.
        try ServerSettings(url: "https://old.example", token: "old-token").save()
        ServerSettings.clearIfReinstalled(dataReadable: true, defaults)
        XCTAssertEqual(ServerSettings.load().url, "")
        XCTAssertTrue(defaults.bool(forKey: ServerSettings.installedKey))

        // An update keeps them: the app's state is there.
        defaults.removePersistentDomain(forName: "SettingsTests.reinstall")
        defaults.set(Date.now, forKey: "lastSuccess")
        try ServerSettings(url: "https://kept.example", token: "t").save()
        ServerSettings.clearIfReinstalled(dataReadable: true, defaults)
        XCTAssertEqual(ServerSettings.load().url, "https://kept.example")

        // Once marked installed, nothing is cleared again.
        defaults.removeObject(forKey: "lastSuccess")
        ServerSettings.clearIfReinstalled(dataReadable: true, defaults)
        XCTAssertEqual(ServerSettings.load().url, "https://kept.example")
    }
}
