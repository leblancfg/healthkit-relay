import XCTest
@testable import HealthKitRelay

final class PairingTests: XCTestCase {
    func testSetupLink() throws {
        let link = try XCTUnwrap(PairingLink(URL(string: "healthrelay://pair?code=ABCD234&server=https%3A%2F%2Fhealth.example.com%3A8180")!))
        XCTAssertEqual(link.server.absoluteString, "https://health.example.com:8180")
        XCTAssertEqual(link.code, "ABCD234")
        for bad in ["healthrelay://pair?code=X", "healthrelay://other?code=X&server=https://h.example",
                    "healthrelay://pair?code=X&server=ftp://h.example", "https://pair?code=X&server=https://h.example",
                    "healthrelay://pair?code=&server=https://h.example"] {
            XCTAssertNil(PairingLink(URL(string: bad)!), bad)
        }
    }

    func testSameServer() {
        let s = ServerSettings(url: "https://Health.example.com:443/", token: "t")
        XCTAssertTrue(s.sameServer(as: "https://health.example.com"))
        XCTAssertFalse(s.sameServer(as: "https://health.example.com:8180"))
        XCTAssertFalse(s.sameServer(as: "http://health.example.com"))
        XCTAssertFalse(ServerSettings(url: "", token: "").sameServer(as: ""))
        XCTAssertEqual(ServerSettings(url: "https://health.example.com:8180", token: "t").host, "health.example.com:8180")
        XCTAssertEqual(ServerSettings(url: "https://health.example.com/relay/", token: "t").baseURL?.absoluteString,
                       "https://health.example.com/relay/")
    }

    func testPlainHTTPOnlyOnTheLocalNetwork() {
        for local in ["http://192.168.1.10:8080", "http://10.0.0.2", "http://172.20.1.1", "http://100.101.102.103:8180",
                      "http://nas.local", "http://nas:8080", "http://[fd7a:115c:a1e0::1]:8080", "http://169.254.3.4"] {
            XCTAssertTrue(ServerSettings(url: local, token: "t").isComplete, local)
        }
        for remote in ["http://203.0.113.9:8080", "http://health.example.com", "http://8.8.8.8", "http://172.32.0.1",
                       "http://3232235777", "http://0x7f000001", "http://[2001:db8::1]"] {
            XCTAssertFalse(ServerSettings(url: remote, token: "t").isComplete, remote)
            XCTAssertTrue(ServerSettings(url: remote.replacingOccurrences(of: "http://", with: "https://"), token: "t").isComplete, remote)
        }
        XCTAssertNil(PairingLink(URL(string: "healthrelay://pair?code=X&server=http%3A%2F%2F203.0.113.9")!))
    }

    func testReadableNames() {
        XCTAssertEqual(SyncEngine.readableName("HKQuantityTypeIdentifierHeartRate"), "Heart rate")
        XCTAssertEqual(SyncEngine.readableName("HKQuantityTypeIdentifierHeartRateVariabilitySDNN"), "Heart rate variability SDNN")
        XCTAssertEqual(SyncEngine.readableName("HKQuantityTypeIdentifierVO2Max"), "VO2 max")
        XCTAssertEqual(SyncEngine.readableName("HKWorkoutTypeIdentifier"), "Workout")
        XCTAssertEqual(SyncEngine.readableName("HKCharacteristicTypeIdentifierDateOfBirth"), "Date of birth")
        XCTAssertEqual(SyncEngine.readableName("HKVisionPrescriptionTypeIdentifier"), "Vision prescription")
        XCTAssertEqual(SyncEngine.readableName("HKDataTypeUserAnnotatedMedicationConcept"), "User annotated medication concept")
    }
}
