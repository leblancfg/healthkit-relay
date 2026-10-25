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
        let v4 = h.split(separator: ".").compactMap { UInt8($0) }
        if v4.count == 4, h.split(separator: ".").count == 4 {
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

    static func load() -> ServerSettings {
        ServerSettings(url: Keychain.get("server-url") ?? "", token: Keychain.get("server-token") ?? "")
    }

    func save() throws {
        try Keychain.set(url.trimmingCharacters(in: .whitespacesAndNewlines), for: "server-url")
        try Keychain.set(token.trimmingCharacters(in: .whitespacesAndNewlines), for: "server-token")
    }

    static func clear() {
        Keychain.delete("server-url")
        Keychain.delete("server-token")
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
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
            kSecReturnData as String: true,
            kSecMatchLimit as String: kSecMatchLimitOne,
        ]
        var item: CFTypeRef?
        guard SecItemCopyMatching(query as CFDictionary, &item) == errSecSuccess,
              let data = item as? Data else { return nil }
        return String(data: data, encoding: .utf8)
    }

    static func delete(_ account: String) {
        SecItemDelete([
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
        ] as CFDictionary)
    }

    static func set(_ value: String, for account: String) throws {
        let base: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
        ]
        SecItemDelete(base as CFDictionary)
        var add = base
        add[kSecValueData as String] = Data(value.utf8)
        add[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
        let status = SecItemAdd(add as CFDictionary, nil)
        guard status == errSecSuccess else {
            throw NSError(domain: NSOSStatusErrorDomain, code: Int(status))
        }
    }
}
