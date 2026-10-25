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

    /// host[:port], for display.
    var host: String { ServerSettings(url: server.absoluteString, token: "").host }

    /// Plain HTTP: the token and health data cross the network unencrypted.
    var isUnencrypted: Bool { server.scheme?.lowercased() == "http" }
}

/// What GET /v1/status says (also part of the pairing answer).
struct ServerStatus: Codable, Equatable, Sendable {
    var version: String
    var wireVersion: Int
    var records: Int
    var device: PairedDevice?
    /// Server 1.0+ (ServerAddress.swift).
    var serverID: String?
    var preferredURL: String?

    enum CodingKeys: String, CodingKey {
        case version, records, device
        case wireVersion = "wire_version"
        case serverID = "server_id"
        case preferredURL = "preferred_url"
    }

    var handoff: Handoff { Handoff(serverID: serverID, preferredURL: preferredURL) }
}

/// What GET /v1/server says, without a token (server 1.0+).
struct ServerInfo: Codable, Equatable, Sendable {
    var serverID: String

    enum CodingKeys: String, CodingKey {
        case serverID = "server_id"
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
        case .unauthorized: "The server did not accept this token. If you typed the admin token, check it: the server printed it in its log on its first start, and healthkit-relay admin reset on the server makes a new one."
        case .tooOld: "This address answers, but not as a healthkit-relay server that can pair an iPhone. Check the server address. If it is right, update healthkit-relay on the server."
        case .eraseUnsupported: "This server is too old to erase data: update healthkit-relay on the server, then try again. Nothing was erased or reset."
        case let .notAllowed(msg): msg
        case let .http(code, msg): "The server answered with error \(code): \(msg)"
        case let .network(msg): msg
        }
    }
}

/// Calls the server's setup endpoints.
struct ServerClient: Sendable {
    let base: URL
    /// Follows no redirect: pairing and status go only to the address the user chose.
    var session: URLSession = AddressHandoff.noRedirects

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
            throw ServerError.network("Could not reach \(base.host() ?? base.absoluteString): \(error.localizedDescription) Check that the server is running, and that this iPhone can reach it: on the same Wi-Fi, or through a VPN such as Tailscale.")
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

    /// Turns the admin token (typed in by hand) into a token of this iPhone's own, so the
    /// admin token does not stay on the phone and the device can be revoked on its own.
    func pair(serverToken: String, replacing old: String?) async throws -> Paired {
        struct Code: Decodable { let code: String }
        let data = try await request("POST", "v1/pairing-codes", token: serverToken, body: ["public_url": base.absoluteString])
        return try await pair(code: try JSONDecoder().decode(Code.self, from: data).code, replacing: old)
    }

    func serverInfo() async throws -> ServerInfo {
        try JSONDecoder().decode(ServerInfo.self, from: try await request("GET", "v1/server", timeout: 15))
    }

    func status(token: String) async throws -> ServerStatus {
        try JSONDecoder().decode(ServerStatus.self, from: try await request("GET", "v1/status", token: token))
    }

    /// The server's answer to DELETE /v1/data.
    struct Erased: Decodable, Equatable, Sendable {
        /// How many stored items the server deleted.
        let erased: Int
        /// The device whose data was deleted; empty for the admin token.
        let device: String?
        /// Asked with `disconnect`: the server stores nothing more sent with this token (it
        /// revoked it, or is a demo server). Nil from a server before 1.0, or for the admin token.
        var disconnected: Bool? = nil
    }

    /// Long: the server deletes a whole history before it answers.
    static let eraseTimeout: TimeInterval = 600

    /// Permanently deletes everything the token's device uploaded.
    /// `disconnect`: the server also revokes this device's token, in the same step, so nothing
    /// sent before can be stored after the erase (a server before 1.0 ignores it).
    func erase(token: String, disconnect: Bool = false) async throws -> Erased {
        let data: Data
        var query = [URLQueryItem(name: "confirm", value: "erase")]
        if disconnect { query.append(URLQueryItem(name: "disconnect", value: "1")) }
        do {
            data = try await request("DELETE", "v1/data", query: query,
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
