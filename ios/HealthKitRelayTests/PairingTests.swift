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
