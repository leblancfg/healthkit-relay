import HealthKit
import XCTest
@testable import HealthKitRelay

final class EncoderTests: XCTestCase {
    private let store = HKHealthStore()
    private lazy var encoder = SampleEncoder(store: store, units: UnitResolver(store: store, defaults: UserDefaults(suiteName: "tests")!))
    private let start = Date(timeIntervalSince1970: 1_767_323_045)

    private func object(_ json: JSON) throws -> [String: Any] {
        try XCTUnwrap(JSONSerialization.jsonObject(with: json.data()) as? [String: Any])
    }

    func testMetadataKeepsTypes() throws {
        let json = JSON.metadata([
            "n": NSNumber(value: 3), "d": NSNumber(value: 1.5), "b": NSNumber(value: true), "s": "x",
            "date": start, "q": HKQuantity(unit: .meter(), doubleValue: 5),
        ])
        let o = try object(try XCTUnwrap(json))
        XCTAssertEqual(o["n"] as? Int, 3)
        XCTAssertEqual(o["d"] as? Double, 1.5)
        XCTAssertEqual(o["b"] as? Bool, true)
        XCTAssertTrue(String(data: try json!.data(), encoding: .utf8)!.contains("\"b\":true"))
        XCTAssertEqual(o["date"] as? String, "2026-01-02T03:04:05.000Z")
        XCTAssertEqual(o["q"] as? [String: AnyHashable], ["value": 5.0, "unit": "m"])
    }

    func testQuantityCodecNeverDrops() {
        let q = HKQuantity(unit: HKUnit.count().unitDivided(by: .minute()), doubleValue: 60)
        XCTAssertEqual(QuantityCodec.encode(q, preferred: .count().unitDivided(by: .second())),
                       ["value": 1.0, "unit": "count/s"])
        // Incompatible preferred unit: falls back to the quantity's own unit.
        XCTAssertEqual(QuantityCodec.encode(q, preferred: .meter()), ["value": 60.0, "unit": "count/min"])
    }

    func testQuantitySample() async throws {
        let s = HKQuantitySample(type: HKQuantityType(.heartRate),
                                 quantity: HKQuantity(unit: HKUnit.count().unitDivided(by: .minute()), doubleValue: 62),
                                 start: start, end: start, metadata: [HKMetadataKeyHeartRateMotionContext: NSNumber(value: 1)])
        let out = await encoder.encode(s)
        let o = try object(out.sample)
        XCTAssertEqual(o["kind"] as? String, "quantity")
        XCTAssertEqual(o["type"] as? String, "HKQuantityTypeIdentifierHeartRate")
        XCTAssertNotNil(o["value"] as? Double)
        XCTAssertNotNil(o["unit"] as? String)
        XCTAssertEqual((o["metadata"] as? [String: Any])?[HKMetadataKeyHeartRateMotionContext] as? Int, 1)
        XCTAssertNotNil(o["source"] as? [String: Any])
        XCTAssertNil(o["archive"], "bulk kinds carry no archive")
    }

    func testCategorySampleLabel() async throws {
        let s = HKCategorySample(type: HKCategoryType(.sleepAnalysis), value: HKCategoryValueSleepAnalysis.asleepDeep.rawValue,
                                 start: start, end: start.addingTimeInterval(1800))
        let o = try object(await encoder.encode(s).sample)
        XCTAssertEqual(o["kind"] as? String, "category")
        XCTAssertEqual(o["value"] as? Int, 4)
        XCTAssertEqual(o["value_label"] as? String, "asleepDeep")
        XCTAssertEqual(o["value_enum"] as? String, "HKCategoryValueSleepAnalysis")
    }

    func testCorrelationCarriesMembers() async throws {
        let mmHg = HKUnit.millimeterOfMercury()
        let sys = HKQuantitySample(type: HKQuantityType(.bloodPressureSystolic), quantity: HKQuantity(unit: mmHg, doubleValue: 120), start: start, end: start)
        let dia = HKQuantitySample(type: HKQuantityType(.bloodPressureDiastolic), quantity: HKQuantity(unit: mmHg, doubleValue: 80), start: start, end: start)
        let c = HKCorrelation(type: HKCorrelationType(.bloodPressure), start: start, end: start, objects: [sys, dia])
        let o = try object(await encoder.encode(c).sample)
        XCTAssertEqual(o["kind"] as? String, "correlation")
        XCTAssertEqual((o["objects"] as? [Any])?.count, 2)
        XCTAssertNotNil(o["archive"] as? String)
    }

    func testWorkout() async throws {
        let config = HKWorkoutConfiguration()
        config.activityType = .running
        let w = HKWorkout(activityType: .running, start: start, end: start.addingTimeInterval(1800))
        let o = try object(await encoder.encode(w).sample)
        XCTAssertEqual(o["kind"] as? String, "workout")
        let summary = try XCTUnwrap(o["workout"] as? [String: Any])
        XCTAssertEqual(summary["activity_type"] as? Int, 37)
        XCTAssertEqual(summary["activity_name"] as? String, "running")
        XCTAssertEqual(summary["duration_s"] as? Double, 1800)
        XCTAssertEqual((o["activity_type"] as? [String: Any])?["name"] as? String, "running")
        XCTAssertNotNil(o["archive"] as? String)
    }

    func testChunkerSplits() {
        let s = HKQuantitySample(type: HKQuantityType(.stepCount), quantity: HKQuantity(unit: .count(), doubleValue: 1), start: start, end: start)
        var c = Chunker(sample: s, series: "quantity")
        var points: [JSON] = [1, 2, 3]
        c.add(&points)
        points = [4]
        let records = c.finish(&points)
        XCTAssertEqual(records.count, 2)
        XCTAssertEqual(records[1]["final"], true)
        XCTAssertEqual(records[1]["total_points"], 4)
        XCTAssertEqual(records[0]["key"], .string("\(s.uuid.uuidString)/quantity/0"))
    }

    func testBlobChunks() {
        let s = HKQuantitySample(type: HKQuantityType(.stepCount), quantity: HKQuantity(unit: .count(), doubleValue: 1), start: start, end: start)
        let data = Data(repeating: 7, count: SampleEncoder.blobBytesPerChunk * 2 + 1)
        let chunks = SampleEncoder.blobChunks(owner: s, blob: "attachment", id: "a", contentType: "image/jpeg", data: data)
        XCTAssertEqual(chunks.count, 3)
        XCTAssertEqual(chunks[2]["chunks"], 3)
    }

    func testBatchBody() throws {
        let batch = WireBatch(device: ["model": "x"], items: [
            try WireItem(.sample, ["uuid": "u"]),
            try WireItem(.record, ["kind": "k", "key": "1"]),
            try WireItem(.deletion, ["uuid": "d"]),
        ])
        let o = try XCTUnwrap(JSONSerialization.jsonObject(with: batch.body()) as? [String: Any])
        XCTAssertEqual(o["version"] as? Int, 2)
        XCTAssertEqual((o["samples"] as? [Any])?.count, 1)
        XCTAssertEqual((o["records"] as? [Any])?.count, 1)
        XCTAssertEqual((o["deletions"] as? [Any])?.count, 1)
        XCTAssertEqual(batch.recordCount, 1)
    }
}
