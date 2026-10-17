import Foundation
import HealthKit

/// JSON body of POST /v1/samples. See server/README.md for the schema.
struct WireBatch: Encodable {
    var device: WireDevice
    var samples: [WireSample]
    var deletions: [WireDeletion]
}

struct WireDevice: Encodable {
    var model: String
    var systemVersion: String
    var appVersion: String

    enum CodingKeys: String, CodingKey {
        case model, systemVersion = "system_version", appVersion = "app_version"
    }
}

struct WireSource: Encodable {
    var name: String
    var bundleID: String
    var version: String?
    var device: String?

    enum CodingKeys: String, CodingKey {
        case name, bundleID = "bundle_id", version, device
    }
}

struct WireWorkout: Encodable {
    var activityType: UInt
    var activityName: String?
    var durationSeconds: Double
    var energyKcal: Double?
    var distanceMeters: Double?

    enum CodingKeys: String, CodingKey {
        case activityType = "activity_type", activityName = "activity_name"
        case durationSeconds = "duration_s", energyKcal = "energy_kcal", distanceMeters = "distance_m"
    }
}

struct WireSample: Encodable {
    var uuid: String
    var type: String
    var kind: String
    var start: String
    var end: String
    var value: Double?
    var unit: String?
    var valueLabel: String?
    var source: WireSource
    var metadata: [String: String]?
    var workout: WireWorkout?

    enum CodingKeys: String, CodingKey {
        case uuid, type, kind, start, end, value, unit, valueLabel = "value_label", source, metadata, workout
    }
}

struct WireDeletion: Encodable {
    var uuid: String
    var type: String
}

enum WireEncoder {
    static func timestamp(_ date: Date) -> String {
        // RFC 3339 in UTC with milliseconds: 2026-01-02T03:04:05.123Z
        date.formatted(Date.ISO8601FormatStyle(includingFractionalSeconds: true))
    }

    static func sample(_ s: HKSample, as synced: SyncedType) -> WireSample {
        let src = s.sourceRevision
        var out = WireSample(
            uuid: s.uuid.uuidString,
            type: s.sampleType.identifier,
            kind: "sample",
            start: timestamp(s.startDate),
            end: timestamp(s.endDate),
            source: WireSource(
                name: src.source.name,
                bundleID: src.source.bundleIdentifier,
                version: src.version,
                device: s.device?.model),
            metadata: metadata(s.metadata))

        switch s {
        case let q as HKQuantitySample:
            out.kind = "quantity"
            if let unit = synced.unit, q.quantity.is(compatibleWith: unit) {
                out.value = q.quantity.doubleValue(for: unit)
                out.unit = unit.unitString
            }
        case let c as HKCategorySample:
            out.kind = "category"
            out.value = Double(c.value)
            out.valueLabel = categoryLabel(c)
        case let w as HKWorkout:
            out.kind = "workout"
            out.workout = WireWorkout(
                activityType: w.workoutActivityType.rawValue,
                activityName: w.workoutActivityType.name,
                durationSeconds: w.duration,
                energyKcal: w.statistics(for: HKQuantityType(.activeEnergyBurned))?
                    .sumQuantity()?.doubleValue(for: .kilocalorie()),
                distanceMeters: distance(w))
        default:
            break
        }
        return out
    }

    static func deletion(_ d: HKDeletedObject, type: HKSampleType) -> WireDeletion {
        WireDeletion(uuid: d.uuid.uuidString, type: type.identifier)
    }

    private static func distance(_ w: HKWorkout) -> Double? {
        let types: [HKQuantityTypeIdentifier] = [.distanceWalkingRunning, .distanceCycling, .distanceSwimming,
                                                 .distanceDownhillSnowSports, .distanceWheelchair]
        for id in types {
            if let q = w.statistics(for: HKQuantityType(id))?.sumQuantity() {
                return q.doubleValue(for: .meter())
            }
        }
        return nil
    }

    private static func categoryLabel(_ c: HKCategorySample) -> String? {
        guard c.categoryType.identifier == HKCategoryTypeIdentifier.sleepAnalysis.rawValue,
              let v = HKCategoryValueSleepAnalysis(rawValue: c.value) else { return nil }
        switch v {
        case .inBed: return "inBed"
        case .asleepUnspecified: return "asleepUnspecified"
        case .awake: return "awake"
        case .asleepCore: return "asleepCore"
        case .asleepDeep: return "asleepDeep"
        case .asleepREM: return "asleepREM"
        @unknown default: return nil
        }
    }

    private static func metadata(_ m: [String: Any]?) -> [String: String]? {
        guard let m, !m.isEmpty else { return nil }
        var out: [String: String] = [:]
        for (k, v) in m {
            switch v {
            case let d as Date: out[k] = timestamp(d)
            case let q as HKQuantity: out[k] = "\(q)"
            default: out[k] = "\(v)"
            }
        }
        return out
    }
}

extension HKWorkoutActivityType {
    /// Readable names for common activities; the raw value is always sent too.
    var name: String? {
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
        case .dance: "dance"
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
