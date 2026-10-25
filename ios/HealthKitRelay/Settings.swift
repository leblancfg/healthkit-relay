import CryptoKit
import Foundation
import Security

/// Server URL and token, kept in the Keychain. Readable after the first unlock following a
/// reboot, so background syncs work while the phone is locked.
struct ServerSettings: Equatable, Sendable {
    var url: String
    var token: String

    var isComplete: Bool { endpoint != nil && !token.isEmpty }

    /// <url>/v1/samples, or nil if the URL is neither https nor http to a local address.
    var endpoint: URL? {
        let trimmed = url.trimmingCharacters(in: .whitespacesAndNewlines)
        guard var c = URLComponents(string: trimmed), let scheme = c.scheme?.lowercased(),
              let host = c.host, !host.isEmpty,
              scheme == "https" || (scheme == "http" && Self.isLocal(host)) else { return nil }
        c.path = c.path.hasSuffix("/") ? c.path + "v1/samples" : c.path + "/v1/samples"
        return c.url
    }

    /// Hosts plain HTTP may go to, as App Transport Security's local networking allows: names
    /// ending in .local or without a dot (192.168.1.10's neighbours, `nas`), and private,
    /// link-local, Tailscale and loopback addresses. A public address takes https.
    static func isLocal(_ host: String) -> Bool {
        let h = host.lowercased().trimmingCharacters(in: CharacterSet(charactersIn: "[]"))
        if h.hasSuffix(".local") { return true }
        // Digits and dots only: an IPv4 address, taken only in its plain form (four decimal
        // numbers without leading zeros). Networking reads 010.010.010.010 as 8.8.8.8.
        if h.allSatisfy({ $0.isASCII && ($0.isNumber || $0 == ".") }) {
            let parts = h.split(separator: ".", omittingEmptySubsequences: false)
            let v4 = parts.compactMap { p -> UInt8? in p.count > 1 && p.hasPrefix("0") ? nil : UInt8(p) }
            guard parts.count == 4, v4.count == 4 else { return false }
            switch (v4[0], v4[1]) {
            case (10, _), (127, _), (192, 168), (169, 254): return true
            case (172, 16...31), (100, 64...127): return true
            default: return false
            }
        }
        if h.contains(":") {
            return h == "::1" || h.hasPrefix("fc") || h.hasPrefix("fd") || h.hasPrefix("fe8") || h.hasPrefix("fe9")
                || h.hasPrefix("fea") || h.hasPrefix("feb")
        }
        // A single label (`nas`), but not a number or hex, which networking reads as an address.
        return !h.isEmpty && !h.contains(".") && !h.allSatisfy(\.isNumber) && !h.hasPrefix("0x")
    }

    /// The server URL without /v1/samples.
    var baseURL: URL? {
        endpoint?.deletingLastPathComponent().deletingLastPathComponent()
    }

    /// host[:port], for display.
    var host: String {
        guard let c = baseURL.flatMap({ URLComponents(url: $0, resolvingAgainstBaseURL: false) }), let host = c.host else { return url }
        return c.port.map { "\(host):\($0)" } ?? host
    }

    /// Names this connection (address and token) without revealing the token: sync positions
    /// an upload carries apply only to the connection it was sent on.
    var connectionKey: String {
        SHA256.hash(data: Data((url + "\n" + token).utf8)).prefix(12).map { String(format: "%02x", $0) }.joined()
    }

    /// Whether both point to the same server (case, default ports and a trailing slash aside).
    func sameServer(as other: String) -> Bool {
        func key(_ s: String) -> String? {
            guard let u = ServerSettings(url: s, token: "x").endpoint,
                  let c = URLComponents(url: u, resolvingAgainstBaseURL: false), let scheme = c.scheme?.lowercased() else { return nil }
            let port = c.port ?? (scheme == "https" ? 443 : 80)
            return "\(scheme)://\(c.host?.lowercased() ?? ""):\(port)\(c.path)"
        }
        return key(url) != nil && key(url) == key(other)
    }

    /// Address and token are one Keychain item, read and written in one step: a reader on
    /// another thread (a background heartbeat, an upload) never pairs one server's address with
    /// another's token. Apps before 1.0 (14) kept two items; they are read until the next save.
    private static let account = "server"
    private static let legacy = ("server-url", "server-token")

    private struct Stored: Codable {
        var url: String
        var token: String
    }

    static func load() -> ServerSettings {
        if let data = Keychain.getData(account) {
            guard let s = try? JSONDecoder().decode(Stored.self, from: data) else { return ServerSettings(url: "", token: "") }
            return ServerSettings(url: s.url, token: s.token)
        }
        return ServerSettings(url: Keychain.get(legacy.0) ?? "", token: Keychain.get(legacy.1) ?? "")
    }

    func save() throws {
        let s = Stored(url: url.trimmingCharacters(in: .whitespacesAndNewlines), token: token.trimmingCharacters(in: .whitespacesAndNewlines))
        try Keychain.set(try JSONEncoder().encode(s), for: Self.account)
        Keychain.delete(Self.legacy.0)
        Keychain.delete(Self.legacy.1)
    }

    static func clear() {
        Keychain.delete(account)
        Keychain.delete(legacy.0)
        Keychain.delete(legacy.1)
    }
}

extension ServerSettings {
    static let installedKey = "installed"

    /// iOS keeps Keychain items after the app is deleted. A new install (no app state in
    /// UserDefaults, which deleting the app removes) starts unpaired: a server address and
    /// token left by an earlier install are cleared. Before the first unlock after a restart
    /// neither can be read, and nothing changes.
    static func clearIfReinstalled(dataReadable: Bool, _ defaults: UserDefaults = .standard) {
        guard !defaults.bool(forKey: installedKey) else { return }
        let earlierState = ["pairedDevice", "lastSuccess", "readRequested", "tokenUpgradeTried"]
            .contains { defaults.object(forKey: $0) != nil }
        let left = load()
        if !earlierState && !left.url.isEmpty {
            clear()
            Log.info("setup: cleared the server settings an earlier install left in the Keychain")
        }
        if earlierState || !left.url.isEmpty || dataReadable { defaults.set(true, forKey: installedKey) }
    }
}

enum Keychain {
    private static let service = Bundle.main.bundleIdentifier ?? "healthkit-relay"

    static func get(_ account: String) -> String? {
        getData(account).flatMap { String(data: $0, encoding: .utf8) }
    }

    static func getData(_ account: String) -> Data? {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
            kSecReturnData as String: true,
            kSecMatchLimit as String: kSecMatchLimitOne,
        ]
        var item: CFTypeRef?
        guard SecItemCopyMatching(query as CFDictionary, &item) == errSecSuccess else { return nil }
        return item as? Data
    }

    static func delete(_ account: String) {
        SecItemDelete([
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
        ] as CFDictionary)
    }

    static func set(_ value: String, for account: String) throws {
        try set(Data(value.utf8), for: account)
    }

    /// Replaces the item's value in place (a reader sees the old value or the new one, never
    /// none), or adds it.
    static func set(_ data: Data, for account: String) throws {
        let base: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
        ]
        let update: [String: Any] = [
            kSecValueData as String: data,
            kSecAttrAccessible as String: kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly,
        ]
        var status = SecItemUpdate(base as CFDictionary, update as CFDictionary)
        if status == errSecItemNotFound {
            status = SecItemAdd(base.merging(update) { $1 } as CFDictionary, nil)
        }
        guard status == errSecSuccess else {
            throw NSError(domain: NSOSStatusErrorDomain, code: Int(status))
        }
    }
}
