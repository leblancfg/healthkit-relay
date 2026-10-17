import Foundation
import HealthKit

/// A JSON value. The wire format is built from these rather than fixed structs, so every
/// HealthKit kind can carry exactly the fields it has.
enum JSON: Encodable, Equatable, Sendable {
    case null
    case bool(Bool)
    case int(Int64)
    case double(Double)
    case string(String)
    case array([JSON])
    case object([String: JSON])

    func encode(to encoder: Encoder) throws {
        switch self {
        case let .object(fields):
            var c = encoder.container(keyedBy: StringKey.self)
            for (k, v) in fields { try c.encode(v, forKey: StringKey(k)) }
        case let .array(items):
            var c = encoder.unkeyedContainer()
            for v in items { try c.encode(v) }
        default:
            var c = encoder.singleValueContainer()
            switch self {
            case .null: try c.encodeNil()
            case let .bool(b): try c.encode(b)
            case let .int(i): try c.encode(i)
            case let .double(d): if d.isFinite { try c.encode(d) } else { try c.encodeNil() }
            case let .string(s): try c.encode(s)
            default: break
            }
        }
    }

    private struct StringKey: CodingKey {
        var stringValue: String
        var intValue: Int? { nil }
        init(_ s: String) { stringValue = s }
        init?(stringValue: String) { self.stringValue = stringValue }
        init?(intValue: Int) { nil }
    }

    /// Sorted keys: identical content always encodes to identical bytes, which the server
    /// relies on to de-duplicate records.
    static func encoder() -> JSONEncoder {
        let e = JSONEncoder()
        e.outputFormatting = [.sortedKeys, .withoutEscapingSlashes]
        return e
    }

    func data() throws -> Data { try JSON.encoder().encode(self) }

    subscript(key: String) -> JSON? {
        if case let .object(o) = self { return o[key] }
        return nil
    }
}

extension JSON: ExpressibleByStringLiteral, ExpressibleByIntegerLiteral, ExpressibleByBooleanLiteral,
    ExpressibleByDictionaryLiteral, ExpressibleByArrayLiteral, ExpressibleByFloatLiteral
{
    init(stringLiteral v: String) { self = .string(v) }
    init(integerLiteral v: Int) { self = .int(Int64(v)) }
    init(floatLiteral v: Double) { self = .double(v) }
    init(booleanLiteral v: Bool) { self = .bool(v) }
    init(dictionaryLiteral elements: (String, JSON)...) {
        self = .object(Dictionary(elements, uniquingKeysWith: { _, b in b }))
    }
    init(arrayLiteral elements: JSON...) { self = .array(elements) }
}

extension JSON {
    init(_ s: String?) { self = s.map(JSON.string) ?? .null }
    init(_ d: Double?) { self = d.map(JSON.double) ?? .null }
    init(_ i: Int) { self = .int(Int64(i)) }
    init(_ b: Bool) { self = .bool(b) }
    init(_ date: Date?) { self = date.map { .string(WireEncoder.timestamp($0)) } ?? .null }

    /// An enum raw value with its SDK name: {"raw": 37, "name": "running"}.
    static func enumValue(_ raw: Int, _ enumName: String) -> JSON {
        var o: [String: JSON] = ["raw": .int(Int64(raw))]
        if let name = HealthTypes.enumName(enumName, raw) { o["name"] = .string(name) }
        return .object(o)
    }

    /// Any value found in HealthKit metadata or FHIR JSON, with its JSON type kept: numbers stay
    /// numbers, booleans booleans; dates become RFC 3339 strings and quantities {value, unit}.
    static func from(_ value: Any) -> JSON {
        switch value {
        case let n as NSNumber:
            if CFGetTypeID(n) == CFBooleanGetTypeID() { return .bool(n.boolValue) }
            switch UInt8(n.objCType.pointee) {
            case UInt8(ascii: "f"), UInt8(ascii: "d"): return .double(n.doubleValue)
            case UInt8(ascii: "Q"): return n.uint64Value > UInt64(Int64.max) ? .double(n.doubleValue) : .int(n.int64Value)
            default: return .int(n.int64Value)
            }
        case let s as String: return .string(s)
        case let d as Date: return .string(WireEncoder.timestamp(d))
        case let q as HKQuantity: return QuantityCodec.encode(q)
        case let u as UUID: return .string(u.uuidString)
        case let u as NSUUID: return .string(u.uuidString)
        case let u as URL: return .string(u.absoluteString)
        case let d as Data: return ["base64": .string(d.base64EncodedString())]
        case let a as [Any]: return .array(a.map(from))
        case let o as [String: Any]: return .object(o.mapValues(from))
        case is NSNull: return .null
        case let tz as TimeZone: return .string(tz.identifier)
        default: return .string(String(describing: value))
        }
    }

    static func metadata(_ m: [String: Any]?) -> JSON? {
        guard let m, !m.isEmpty else { return nil }
        return .object(m.mapValues(from))
    }

    /// NSKeyedArchiver archive of a HealthKit object, base64: every property HealthKit stores,
    /// including ones this app does not know about yet. Decodable on any Apple platform.
    static func archive(_ object: NSObject & NSSecureCoding) -> JSON {
        var data: Data?
        _ = HKRTry { data = try? NSKeyedArchiver.archivedData(withRootObject: object, requiringSecureCoding: true) }
        return data.map { .string($0.base64EncodedString()) } ?? .null
    }
}

extension Dictionary where Key == String, Value == JSON {
    /// Sets key unless value is nil or .null, keeping objects small.
    mutating func put(_ key: String, _ value: JSON?) {
        guard let value, value != .null else { return }
        self[key] = value
    }
}
