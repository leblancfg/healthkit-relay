import Foundation
import HealthKit

// Wire format v2: POST /v1/samples with
//   {"version": 2, "device": {...}, "samples": [...], "records": [...], "deletions": [...]}
// samples:   every HealthKit sample, any kind, keyed by its UUID (see SampleEncoder).
// records:   everything without a sample UUID, keyed by (kind, key): series points and file
//            chunks of large samples, daily activity summaries, characteristics, medications.
// deletions: tombstones for samples HealthKit reports deleted.
// server/README.md documents every field. Version 1 (app 0.1) had no "version" and no "records".

enum Wire {
    static let version = 2
}

/// The part of a batch an item belongs to.
enum WireSection: Sendable {
    case sample, record, deletion
}

/// One encoded item, kept as bytes so batches can be cut to a size limit.
struct WireItem: Sendable {
    let section: WireSection
    let data: Data

    init(_ section: WireSection, _ json: JSON) throws {
        self.section = section
        data = try json.data()
    }
}

struct WireBatch {
    var device: JSON
    var items: [WireItem]

    var count: Int { items.count }
    var recordCount: Int { items.filter { $0.section == .record }.count }

    /// Assembles the body from pre-encoded items: no second encoding pass.
    func body() throws -> Data {
        var out = Data("{\"version\":\(Wire.version),\"device\":".utf8)
        out.append(try device.data())
        for (name, section) in [("samples", WireSection.sample), ("records", .record), ("deletions", .deletion)] {
            out.append(Data(",\"\(name)\":[".utf8))
            var first = true
            for item in items where item.section == section {
                if !first { out.append(UInt8(ascii: ",")) }
                out.append(item.data)
                first = false
            }
            out.append(UInt8(ascii: "]"))
        }
        out.append(UInt8(ascii: "}"))
        return out
    }
}

enum WireEncoder {
    static func timestamp(_ date: Date) -> String {
        // RFC 3339 in UTC with milliseconds: 2026-01-02T03:04:05.123Z
        date.formatted(Date.ISO8601FormatStyle(includingFractionalSeconds: true))
    }

    static func deletion(_ d: HKDeletedObject, type: HKSampleType) -> JSON {
        var o: [String: JSON] = ["uuid": .string(d.uuid.uuidString), "type": .string(type.identifier)]
        o.put("metadata", JSON.metadata(d.metadata))
        return .object(o)
    }

    static func source(_ s: HKSample) -> JSON {
        let rev = s.sourceRevision
        var o: [String: JSON] = [
            "name": .string(rev.source.name),
            "bundle_id": .string(rev.source.bundleIdentifier),
        ]
        o.put("version", JSON(rev.version))
        o.put("product_type", JSON(rev.productType))
        let os = rev.operatingSystemVersion
        if os.majorVersion > 0 {
            o["os_version"] = .string("\(os.majorVersion).\(os.minorVersion).\(os.patchVersion)")
        }
        o.put("device", JSON(s.device?.model))  // v1 field
        return .object(o)
    }

    static func device(_ d: HKDevice?) -> JSON? {
        guard let d else { return nil }
        var o: [String: JSON] = [:]
        o.put("name", JSON(d.name))
        o.put("manufacturer", JSON(d.manufacturer))
        o.put("model", JSON(d.model))
        o.put("hardware_version", JSON(d.hardwareVersion))
        o.put("firmware_version", JSON(d.firmwareVersion))
        o.put("software_version", JSON(d.softwareVersion))
        o.put("local_identifier", JSON(d.localIdentifier))
        o.put("udi_device_identifier", JSON(d.udiDeviceIdentifier))
        return o.isEmpty ? nil : .object(o)
    }
}

extension HKWorkoutActivityType {
    /// v1 names for common activities, kept so metric series do not change name. The SDK
    /// name of every activity is sent as `activity_type_name`.
    var v1Name: String? {
        switch self {
        case .running: "running"
        case .walking: "walking"
        case .hiking: "hiking"
        case .cycling: "cycling"
        case .swimming: "swimming"
        case .rowing: "rowing"
        case .elliptical: "elliptical"
        case .stairClimbing: "stair_climbing"
        case .yoga: "yoga"
        case .pilates: "pilates"
        case .traditionalStrengthTraining: "strength_training"
        case .functionalStrengthTraining: "functional_strength_training"
        case .highIntensityIntervalTraining: "hiit"
        case .coreTraining: "core_training"
        case .crossTraining: "cross_training"
        case .mixedCardio: "mixed_cardio"
        case .soccer: "soccer"
        case .tennis: "tennis"
        case .downhillSkiing: "downhill_skiing"
        case .crossCountrySkiing: "cross_country_skiing"
        case .snowboarding: "snowboarding"
        case .climbing: "climbing"
        case .mindAndBody: "mind_and_body"
        case .cooldown: "cooldown"
        case .other: "other"
        default: nil
        }
    }
}
