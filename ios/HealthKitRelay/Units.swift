import Foundation
import HealthKit

/// Turns HKQuantity into {"value": 62, "unit": "count/min"}. Values are never dropped for want
/// of a unit: the preferred unit if compatible, else the quantity's own unit, else the first
/// compatible base unit, else the quantity's text.
enum QuantityCodec {
    /// Base units tried last, broad enough to cover every dimension HealthKit uses.
    private static let probeNames: [String] = [
        "count", "%", "m", "g", "s", "degC", "L", "kcal", "J", "mmHg", "Pa", "W", "Hz", "S", "V",
        "dBASPL", "dBHL", "IU", "appleEffortScore", "lx", "D", "deg", "rad",
        "count/min", "m/s", "mg/dL", "mL/(kg*min)", "kcal/(kg*hr)", "L/min", "mcV",
    ]
    private static let probes: [HKUnit] = probeNames.compactMap { HKRUnitFromString($0) }

    static func split(_ q: HKQuantity, preferred: HKUnit? = nil) -> (value: Double, unit: String)? {
        if let preferred, q.is(compatibleWith: preferred) {
            return (q.doubleValue(for: preferred), preferred.unitString)
        }
        if let own = ownUnit(q) {
            return (q.doubleValue(for: own), own.unitString)
        }
        for unit in probes where q.is(compatibleWith: unit) {
            return (q.doubleValue(for: unit), unit.unitString)
        }
        return nil
    }

    static func encode(_ q: HKQuantity?, preferred: HKUnit? = nil) -> JSON {
        guard let q else { return .null }
        guard let parts = split(q, preferred: preferred) else {
            return ["text": .string(String(describing: q))]
        }
        return ["value": .double(parts.value), "unit": .string(parts.unit)]
    }

    /// The unit the quantity was created with: its description is "<value> <unit>".
    static func ownUnit(_ q: HKQuantity) -> HKUnit? {
        let text = String(describing: q)
        guard let space = text.firstIndex(of: " ") else { return nil }
        let unitString = String(text[text.index(after: space)...]).trimmingCharacters(in: .whitespaces)
        guard let unit = HKRUnitFromString(unitString), q.is(compatibleWith: unit) else { return nil }
        return unit
    }
}

/// Picks one unit per quantity type and keeps it ("pins" it), so a type's values stay
/// comparable for its whole history whatever later happens to the phone's region settings.
/// Order: the pinned unit, the unit the Health app shows (preferredUnits), Apple's documented
/// canonical unit from the SDK headers, then any compatible base unit.
actor UnitResolver {
    private let store: HKHealthStore
    private let defaults: UserDefaults
    private let key = "units.v2"
    private var cache: [String: HKUnit] = [:]

    init(store: HKHealthStore, defaults: UserDefaults = .standard) {
        self.store = store
        self.defaults = defaults
    }

    func unit(for type: HKQuantityType) async -> HKUnit? {
        let id = type.identifier
        if let unit = cache[id] { return unit }
        var pinned = defaults.dictionary(forKey: key) as? [String: String] ?? [:]
        if let s = pinned[id], let unit = HKRUnitFromString(s), type.is(compatibleWith: unit) {
            cache[id] = unit
            return unit
        }
        var chosen: HKUnit?
        if let preferred = try? await store.preferredUnits(for: [type])[type], type.is(compatibleWith: preferred) {
            chosen = preferred
        }
        if chosen == nil, let s = HealthTypes.named(id)?.canonicalUnit, let unit = HKRUnitFromString(s),
           type.is(compatibleWith: unit) {
            chosen = unit
        }
        if chosen == nil {
            for s in ["count", "%", "m", "g", "s", "degC", "L", "kcal", "count/min", "dBASPL", "appleEffortScore"] {
                if let unit = HKRUnitFromString(s), type.is(compatibleWith: unit) { chosen = unit; break }
            }
        }
        if let chosen {
            cache[id] = chosen
            pinned[id] = chosen.unitString
            defaults.set(pinned, forKey: key)
        }
        return chosen
    }

    func reset() {
        cache = [:]
        defaults.removeObject(forKey: key)
    }
}
