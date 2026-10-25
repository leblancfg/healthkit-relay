import HealthKit
import XCTest
@testable import HealthKitRelay

final class HealthTypesTests: XCTestCase {
    func testCatalogCoversTheSDK() {
        let catalog = HealthTypes.catalog
        // Everything the SDK declares resolves, except what needs a newer iOS than this one.
        XCTAssertEqual(catalog.types.count + catalog.unavailable.count + catalog.leftOut.count,
                       SDKHealthTypes.identified.count + SDKHealthTypes.factories.count)
        // App Store builds read no clinical data (Health Records, CDA documents).
        #if !HKR_HEALTH_RECORDS
        XCTAssertFalse(catalog.types.contains { $0.type is HKClinicalType || $0.key == "HKDocumentTypeIdentifierCDA" })
        XCTAssertTrue(catalog.leftOut.contains("HKClinicalTypeIdentifierAllergyRecord"))
        #endif
        XCTAssertGreaterThan(catalog.types.count, 200)
        for id in ["HKQuantityTypeIdentifierHeartRate", "HKCategoryTypeIdentifierSleepAnalysis",
                   "HKWorkoutTypeIdentifier", "HKWorkoutRouteTypeIdentifier", "HKDataTypeIdentifierHeartbeatSeries",
                   "HKCorrelationTypeIdentifierBloodPressure", "HKCharacteristicTypeIdentifierDateOfBirth"] {
            XCTAssertNotNil(HealthTypes.named(id), id)
        }
        XCTAssertNotNil(HealthTypes.named(HKObjectType.electrocardiogramType().identifier))
        XCTAssertNotNil(HealthTypes.named(HKObjectType.audiogramSampleType().identifier))
        XCTAssertNotNil(HealthTypes.named(HKObjectType.activitySummaryType().identifier))
        XCTAssertEqual(HealthTypes.uncoveredIdentifiers, [])
    }

    func testCanonicalUnitsParse() {
        for t in HealthTypes.all {
            guard let q = t.type as? HKQuantityType, let s = t.canonicalUnit, !s.isEmpty else { continue }
            let unit = HKRUnitFromString(s)
            XCTAssertNotNil(unit, "\(t.key): \(s)")
            if let unit { XCTAssertTrue(q.is(compatibleWith: unit), "\(t.key): \(s)") }
        }
    }

    func testEnumNames() {
        XCTAssertEqual(HealthTypes.enumName("HKCategoryValueSleepAnalysis", 3), "asleepCore")
        XCTAssertEqual(HealthTypes.enumName("HKCategoryValueSleepAnalysis", 1), "asleepUnspecified")
        XCTAssertEqual(HealthTypes.enumName("HKWorkoutActivityType", 37), "running")
        XCTAssertEqual(HealthTypes.enumName("HKBloodType", 1), "APositive")
        XCTAssertEqual(HealthTypes.named("HKCategoryTypeIdentifierHeadache")?.valueEnum, "HKCategoryValueSeverity")
    }

    func testObjCHelpers() {
        XCTAssertNil(HKRUnitFromString("not a unit"))
        XCTAssertEqual(HKRUnitFromString("count/min")?.unitString, "count/min")
        XCTAssertNil(HKRCallClassMethod("HKObjectType", "quantityTypeForIdentifier:", "HKQuantityTypeIdentifierNope" as NSString))
        XCTAssertNil(HKRCallClassMethod("NoSuchClass", "workoutType", nil))
        XCTAssertNotNil(HKRTry { NSArray().object(at: 1) })
    }

    /// Runs the real authorization probe (it shows nothing) and prints what HealthKit allows.
    func testAuthorizationPlan() async {
        guard HKHealthStore.isHealthDataAvailable() else { return }
        let plan = await SyncEngine.shared.authPlan()
        print("auth plan: read \(plan.read.count), per-object \(plan.perObject.map(\.identifier)), excluded \(plan.excluded)")
        XCTAssertGreaterThan(plan.read.count, 180)
        XCTAssertTrue(plan.read.contains(HKQuantityType(.heartRate)))
    }
}
