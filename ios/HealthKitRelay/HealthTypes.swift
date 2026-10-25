import Foundation
import HealthKit

// The types to sync are not listed by hand: Scripts/gen-health-types writes every type the SDK
// declares into Generated/SDKHealthTypes.swift at build time, and this file resolves them on
// the running iOS. A type the running iOS does not know is skipped, never a crash.

struct SDKFamily: Sendable {
    let className: String
    let selector: String
}

struct SDKIdentifiedType: Sendable {
    let family: String
    let identifier: String
    let ios: String
    /// Apple's documented canonical unit (quantity types), e.g. "count/s".
    let unit: String?
    let aggregation: String?
    /// The NS_ENUM naming this category type's values, e.g. "HKCategoryValueSleepAnalysis".
    let valueEnum: String?
}

struct SDKFactoryType: Sendable {
    let className: String
    let selector: String
    let ios: String
}

/// One HealthKit type, resolved on this iOS.
struct SyncedType: @unchecked Sendable {
    let type: HKObjectType
    let canonicalUnit: String?
    let valueEnum: String?

    /// Stable key for persisted state (anchors, units).
    var key: String { type.identifier }
    var sampleType: HKSampleType? { type as? HKSampleType }
}

enum HealthTypes {
    struct Catalog: @unchecked Sendable {
        /// Every type available on this iOS, in SDK order.
        let types: [SyncedType]
        /// Identifiers the SDK declares that this iOS does not provide.
        let unavailable: [String]
        /// Clinical identifiers this build leaves out (see `isClinical`).
        let leftOut: [String]
        let byKey: [String: SyncedType]
    }

    static let catalog = resolve()
    static var all: [SyncedType] { catalog.types }

    static func named(_ identifier: String) -> SyncedType? { catalog.byKey[identifier] }

    /// Readable name of an enum raw value, from the SDK headers: ("HKWorkoutActivityType", 37) -> "running".
    static func enumName(_ enumName: String?, _ value: Int) -> String? {
        guard let enumName else { return nil }
        return SDKHealthTypes.enums[enumName]?[value]
    }

    static func resolve() -> Catalog {
        var types: [SyncedType] = []
        var unavailable: [String] = []
        var leftOut: [String] = []
        var seen = Set<String>()

        func add(_ object: Any?, unit: String? = nil, valueEnum: String? = nil, fallbackName: String) {
            guard let type = object as? HKObjectType else {
                unavailable.append(fallbackName)
                return
            }
            guard seen.insert(type.identifier).inserted else { return }
            types.append(SyncedType(type: type, canonicalUnit: unit, valueEnum: valueEnum))
        }

        for t in SDKHealthTypes.identified {
            if isClinical(t.family, t.identifier) {
                leftOut.append(t.identifier)
                continue
            }
            guard isAvailable(t.ios), let family = SDKHealthTypes.families[t.family] else {
                unavailable.append(t.identifier)
                continue
            }
            add(HKRCallClassMethod(family.className, family.selector, t.identifier as NSString),
                unit: t.unit, valueEnum: t.valueEnum, fallbackName: t.identifier)
        }
        for f in SDKHealthTypes.factories {
            guard isAvailable(f.ios) else {
                unavailable.append("\(f.className).\(f.selector)")
                continue
            }
            add(HKRCallClassMethod(f.className, f.selector, nil), fallbackName: "\(f.className).\(f.selector)")
        }
        let byKey = Dictionary(types.map { ($0.key, $0) }, uniquingKeysWith: { a, _ in a })
        return Catalog(types: types, unavailable: unavailable, leftOut: leftOut, byKey: byKey)
    }

    /// Clinical data (Health Records and CDA documents): read only by builds with the
    /// HKR_HEALTH_RECORDS condition, which also need the Clinical Health Records capability
    /// and its usage string (ios/README.md). App Store builds leave them out.
    static func isClinical(_ family: String, _ identifier: String) -> Bool {
        #if HKR_HEALTH_RECORDS
        return false
        #else
        return family == "HKClinicalTypeIdentifier" || identifier == "HKDocumentTypeIdentifierCDA"
        #endif
    }

    /// Plain identifier constants with no matching type: an SDK addition the factories missed.
    /// Constant names and values can differ by "Identifier" (HKDataTypeIdentifierStateOfMind is
    /// "HKDataTypeStateOfMind"), so both are compared without it.
    static var uncoveredIdentifiers: [String] {
        let have = Set((catalog.types.map(\.key) + catalog.leftOut).map { $0.replacingOccurrences(of: "Identifier", with: "") })
        return SDKHealthTypes.namedIdentifiers.filter { !have.contains($0.replacingOccurrences(of: "Identifier", with: "")) }
    }

    static func isAvailable(_ ios: String) -> Bool {
        let parts = ios.split(separator: ".").compactMap { Int($0) }
        let version = OperatingSystemVersion(
            majorVersion: parts.first ?? 0,
            minorVersion: parts.count > 1 ? parts[1] : 0,
            patchVersion: parts.count > 2 ? parts[2] : 0)
        return ProcessInfo.processInfo.isOperatingSystemAtLeast(version)
    }
}
