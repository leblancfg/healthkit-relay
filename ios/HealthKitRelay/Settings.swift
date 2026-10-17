import Foundation
import Security

/// Server URL and token, kept in the Keychain. Readable after the first unlock following a
/// reboot, so background syncs work while the phone is locked.
struct ServerSettings: Equatable, Sendable {
    var url: String
    var token: String

    var isComplete: Bool { endpoint != nil && !token.isEmpty }

    /// <url>/v1/samples, or nil if the URL is not http(s).
    var endpoint: URL? {
        let trimmed = url.trimmingCharacters(in: .whitespacesAndNewlines)
        guard var c = URLComponents(string: trimmed), let scheme = c.scheme?.lowercased(),
              ["https", "http"].contains(scheme), c.host?.isEmpty == false else { return nil }
        c.path = c.path.hasSuffix("/") ? c.path + "v1/samples" : c.path + "/v1/samples"
        return c.url
    }

    static func load() -> ServerSettings {
        ServerSettings(url: Keychain.get("server-url") ?? "", token: Keychain.get("server-token") ?? "")
    }

    func save() throws {
        try Keychain.set(url.trimmingCharacters(in: .whitespacesAndNewlines), for: "server-url")
        try Keychain.set(token.trimmingCharacters(in: .whitespacesAndNewlines), for: "server-token")
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
