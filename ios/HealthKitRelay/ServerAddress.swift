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

/// What the paired server said about itself in its last authenticated answer (status, pairing,
/// heartbeat): its server ID and its preferred https address. A setup link for that address
/// is a link for this same server.
enum KnownServer {
    static let idKey = "serverID"
    static let preferredKey = "serverPreferredURL"

    static var id: String? {
        get { UserDefaults.standard.string(forKey: idKey) }
        set { UserDefaults.standard.set(newValue, forKey: idKey) }
    }

    static var preferredURL: String? {
        get { UserDefaults.standard.string(forKey: preferredKey) }
        set { UserDefaults.standard.set(newValue, forKey: preferredKey) }
    }

    static func note(_ handoff: Handoff) {
        if let id = handoff.serverID, !id.isEmpty { Self.id = id }
        if let url = handoff.preferredURL, url.lowercased().hasPrefix("https://") { preferredURL = url }
    }

    static func forget() {
        id = nil
        preferredURL = nil
    }

    /// Whether `url` reaches the server this app is paired with: its current address, or the
    /// preferred address the server itself named. Anything else is another server, whatever it
    /// answers: the server ID is public, so only the paired server's own word counts.
    static func isCurrent(_ url: URL, settings: ServerSettings) -> Bool {
        guard settings.isComplete else { return false }
        if settings.sameServer(as: url.absoluteString) { return true }
        guard let preferredURL else { return false }
        return ServerSettings(url: preferredURL, token: "x").sameServer(as: url.absoluteString)
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

    /// The settings to move to when the preferred address answered as this server, nil when the
    /// app stays. Saves nothing: AppModel.adoptAddress does, on the main actor.
    static func follow(_ handoff: Handoff, from settings: ServerSettings, device: String?,
                       session: URLSession = noRedirects, defaults: UserDefaults = .standard,
                       now: Date = .now) async -> ServerSettings? {
        // Only the answer of the server the app is still paired with counts.
        guard ServerSettings.load() == settings else { return nil }
        KnownServer.note(handoff)
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
            guard ServerSettings.load() == settings else { throw Refusal.settingsChanged }
            let status = try await client.status(token: settings.token)
            guard status.serverID == id else { throw Refusal.differentServer }
            if let device, status.device?.id != device { throw Refusal.differentDevice }
            tried[preferred] = nil
            defaults.set(tried, forKey: triedKey)
            return candidate
        } catch {
            Log.info("server address: staying at \(settings.host), \(candidate.host) did not answer as this server: \(error)")
            return nil
        }
    }

    /// A session that follows no redirect, for every request that carries the token, a setup
    /// code or Health data outside the background session: they go only to the server's own
    /// address. A redirect answer (3xx) counts as an error.
    nonisolated(unsafe) static var noRedirects = session() // tests swap in a stubbed one

    static func session(_ configuration: URLSessionConfiguration = .ephemeral) -> URLSession {
        URLSession(configuration: configuration, delegate: RefuseRedirects(), delegateQueue: nil)
    }

    private final class RefuseRedirects: NSObject, URLSessionTaskDelegate, Sendable {
        func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse,
                        newRequest request: URLRequest) async -> URLRequest? { nil }
    }
}
