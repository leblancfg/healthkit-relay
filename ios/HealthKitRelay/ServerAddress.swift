import Foundation

/// Where the server asks to be reached: part of GET /v1/status and of each heartbeat's answer
/// (server 1.0+). `serverID` names the server's archive; `preferredURL` is its -public-url,
/// sent only when it is https.
struct Handoff: Codable, Equatable, Sendable {
    var serverID: String?
    var preferredURL: String?

    enum CodingKeys: String, CodingKey {
        case serverID = "server_id"
        case preferredURL = "preferred_url"
    }
}

/// Moves the app to the server's preferred address on its own. The server, reached at the
/// current address with this iPhone's token, names its preferred address and its server ID;
/// the app switches once that address answers over HTTPS with the same server ID (GET
/// /v1/server, without the token) and accepts the token as the same device (GET /v1/status).
/// Sync positions stay: it is the same server, with the same history.
enum AddressHandoff {
    /// A preferred address that did not answer as the same server is tried again after this.
    static let retryAfter: TimeInterval = 6 * 60 * 60
    static let triedKey = "addressHandoffTried"

    enum Refusal: Error, Equatable {
        case notHTTPS, differentServer, differentDevice, settingsChanged, redirected
    }

    /// The settings to use from now on when the app moved (already saved), nil when it stays.
    static func follow(_ handoff: Handoff, from settings: ServerSettings, device: String?,
                       session: URLSession = noRedirects, defaults: UserDefaults = .standard,
                       now: Date = .now) async -> ServerSettings? {
        guard let id = handoff.serverID, !id.isEmpty, let preferred = handoff.preferredURL,
              settings.isComplete, !settings.sameServer(as: preferred) else { return nil }
        var tried = defaults.dictionary(forKey: triedKey) as? [String: Date] ?? [:]
        if let last = tried[preferred], now.timeIntervalSince(last) < retryAfter { return nil }
        tried = tried.filter { now.timeIntervalSince($0.value) < retryAfter }
        tried[preferred] = now
        defaults.set(tried, forKey: triedKey)

        let candidate = ServerSettings(url: preferred, token: settings.token)
        do {
            guard candidate.isComplete, let base = candidate.baseURL, base.scheme?.lowercased() == "https" else {
                throw Refusal.notHTTPS
            }
            let client = ServerClient(base: base, session: session)
            guard try await client.serverInfo().serverID == id else { throw Refusal.differentServer }
            let status = try await client.status(token: settings.token)
            guard status.serverID == id else { throw Refusal.differentServer }
            if let device, status.device?.id != device { throw Refusal.differentDevice }
            // A setup that ran meanwhile (a new link) wins.
            guard ServerSettings.load() == settings else { throw Refusal.settingsChanged }
            try candidate.save()
            tried[preferred] = nil
            defaults.set(tried, forKey: triedKey)
            Log.info("server address: moved from \(settings.host) to \(candidate.host), as the server asked")
            return candidate
        } catch {
            Log.info("server address: staying at \(settings.host), \(candidate.host) did not answer as this server: \(error)")
            return nil
        }
    }

    /// A session that follows no redirect: the token goes only to the address the server named.
    static let noRedirects = session()

    static func session(_ configuration: URLSessionConfiguration = .ephemeral) -> URLSession {
        URLSession(configuration: configuration, delegate: RefuseRedirects(), delegateQueue: nil)
    }

    private final class RefuseRedirects: NSObject, URLSessionTaskDelegate, Sendable {
        func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse,
                        newRequest request: URLRequest) async -> URLRequest? { nil }
    }
}
