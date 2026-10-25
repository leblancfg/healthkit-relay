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

    func testAddressAndTokenAreOneItem() throws {
        let saved = ServerSettings.load()
        defer { if saved.url.isEmpty { ServerSettings.clear() } else { try? saved.save() } }
        // Two items, as apps before 1.0 (14) kept them: read as they are.
        ServerSettings.clear()
        try Keychain.set("https://legacy.example", for: "server-url")
        try Keychain.set("legacy-token", for: "server-token")
        XCTAssertEqual(ServerSettings.load(), ServerSettings(url: "https://legacy.example", token: "legacy-token"))
        // The next save writes one item and drops the two.
        let new = ServerSettings(url: "https://new.example", token: "new-token")
        try new.save()
        XCTAssertNil(Keychain.get("server-url"))
        XCTAssertNil(Keychain.get("server-token"))
        XCTAssertEqual(ServerSettings.load(), new)
        try ServerSettings(url: "https://other.example", token: "other").save()
        XCTAssertEqual(ServerSettings.load(), ServerSettings(url: "https://other.example", token: "other"))
        ServerSettings.clear()
        XCTAssertEqual(ServerSettings.load(), ServerSettings(url: "", token: ""))
    }
}
