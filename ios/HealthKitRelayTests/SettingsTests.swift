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
}
