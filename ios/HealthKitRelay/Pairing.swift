import Foundation
import UIKit

/// A setup link from the server: healthrelay://pair?server=https://…&code=….
struct PairingLink: Equatable, Sendable {
    let server: URL
    let code: String

    static let scheme = "healthrelay"

    init?(_ url: URL) {
        guard url.scheme?.lowercased() == Self.scheme, url.host?.lowercased() == "pair",
              let items = URLComponents(url: url, resolvingAgainstBaseURL: false)?.queryItems,
              let serverText = items.first(where: { $0.name == "server" })?.value,
              let code = items.first(where: { $0.name == "code" })?.value, !code.isEmpty,
              let server = URL(string: serverText), ServerSettings(url: serverText, token: "x").isComplete
        else { return nil }
        self.server = server
        self.code = code
    }
}

/// What GET /v1/status says (also part of the pairing answer).
struct ServerStatus: Codable, Equatable, Sendable {
    var version: String
    var wireVersion: Int
    var records: Int
    var device: PairedDevice?

    enum CodingKeys: String, CodingKey {
        case version, records, device
        case wireVersion = "wire_version"
    }
}

/// This app's entry in the server's device list.
struct PairedDevice: Codable, Equatable, Sendable {
    var id: String
    var name: String
    var model: String?

    private static let key = "pairedDevice"

    static func load() -> PairedDevice? {
        UserDefaults.standard.data(forKey: key).flatMap { try? JSONDecoder().decode(PairedDevice.self, from: $0) }
    }

    static func save(_ device: PairedDevice?) {
        UserDefaults.standard.set(device.flatMap { try? JSONEncoder().encode($0) }, forKey: key)
    }
}

enum ServerError: LocalizedError, Equatable {
    case badCode(String)
    case unauthorized
    case tooOld
    case notAllowed(String)
    case http(Int, String)
    case network(String)
    /// DELETE /v1/data answered 404 or 405: a server from before erasing existed.
    case eraseUnsupported

    var errorDescription: String? {
        switch self {
        case let .badCode(msg): msg
        case .unauthorized: "The server did not accept this token."
        case .tooOld: "This server is too old for one-tap setup: update healthkit-relay."
        case .eraseUnsupported: "This server is too old to erase data: update healthkit-relay on the server, then try again. Nothing was erased or reset."
        case let .notAllowed(msg): msg
        case let .http(code, msg): "The server answered \(code): \(msg)"
        case let .network(msg): msg
        }
    }
}

/// Calls the server's setup endpoints.
struct ServerClient: Sendable {
    let base: URL
    var session: URLSession = .shared

    private func request(_ method: String, _ path: String, query: [URLQueryItem] = [], token: String? = nil,
                         body: [String: String]? = nil, timeout: TimeInterval = 20) async throws -> Data {
        var url = base.appending(path: path)
        if !query.isEmpty { url.append(queryItems: query) }
        var req = URLRequest(url: url, timeoutInterval: timeout)
        req.httpMethod = method
        if let token { req.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization") }
        if let body {
            req.setValue("application/json", forHTTPHeaderField: "Content-Type")
            req.httpBody = try JSONSerialization.data(withJSONObject: body)
        }
        let data: Data, response: URLResponse
        do {
            (data, response) = try await session.data(for: req)
        } catch {
            throw ServerError.network("Could not reach \(base.host() ?? base.absoluteString): \(error.localizedDescription)")
        }
        let code = (response as? HTTPURLResponse)?.statusCode ?? 0
        guard (200..<300).contains(code) else {
            let message = (try? JSONSerialization.jsonObject(with: data) as? [String: Any])?["error"] as? String
                ?? String(decoding: data.prefix(200), as: UTF8.self)
            switch code {
            case 401: throw ServerError.unauthorized
            case 404, 405: throw ServerError.tooOld
            default: throw ServerError.http(code, message)
            }
        }
        return data
    }

    struct Paired: Decodable, Sendable {
        let token: String
        let device: PairedDevice
        let server: ServerStatus
    }

    /// Exchanges a setup code for this iPhone's own token.
    func pair(code: String, replacing old: String?) async throws -> Paired {
        var body = ["code": code, "name": await UIDevice.current.name, "model": Self.model]
        if let old, !old.isEmpty { body["replaces"] = old }
        do {
            return try JSONDecoder().decode(Paired.self, from: try await request("POST", "v1/pair", body: body))
        } catch let ServerError.http(403, message) {
            throw message.contains("private networks") ? ServerError.notAllowed(message) : ServerError.badCode(message)
        }
    }

    /// Turns the server token (typed in by hand) into a token of this iPhone's own, so the
    /// server token does not stay on the phone and the device can be revoked on its own.
    func pair(serverToken: String, replacing old: String?) async throws -> Paired {
        struct Code: Decodable { let code: String }
        let data = try await request("POST", "v1/pairing-codes", token: serverToken, body: ["public_url": base.absoluteString])
        return try await pair(code: try JSONDecoder().decode(Code.self, from: data).code, replacing: old)
    }

    func status(token: String) async throws -> ServerStatus {
        try JSONDecoder().decode(ServerStatus.self, from: try await request("GET", "v1/status", token: token))
    }

    /// The server's answer to DELETE /v1/data.
    struct Erased: Decodable, Equatable, Sendable {
        /// How many stored items the server deleted.
        let erased: Int
        /// The device whose data was deleted; empty for the server token.
        let device: String?
    }

    /// Long: the server deletes a whole history before it answers.
    static let eraseTimeout: TimeInterval = 600

    /// Permanently deletes everything the token's device uploaded.
    func erase(token: String) async throws -> Erased {
        let data: Data
        do {
            data = try await request("DELETE", "v1/data", query: [URLQueryItem(name: "confirm", value: "erase")],
                                 token: token, timeout: Self.eraseTimeout)
        } catch ServerError.tooOld {
            throw ServerError.eraseUnsupported
        }
        do {
            return try JSONDecoder().decode(Erased.self, from: data)
        } catch {
            // Not the answer of a server that erased: nothing may be reset on it.
            throw ServerError.http(200, "unexpected answer: \(String(decoding: data.prefix(200), as: UTF8.self))")
        }
    }

    static var model: String {
        var info = utsname()
        uname(&info)
        return withUnsafeBytes(of: &info.machine) { String(decoding: $0.prefix(while: { $0 != 0 }), as: UTF8.self) }
    }
}
