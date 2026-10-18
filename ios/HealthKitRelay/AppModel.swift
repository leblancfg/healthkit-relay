import Foundation
import UIKit

/// Everything the app decides on its own: setting up from a link, asking for Health access
/// when needed, syncing when opened. The screens only show its state.
@MainActor
final class AppModel: ObservableObject {
    static let shared = AppModel()

    @Published private(set) var settings = ServerSettings.load()
    @Published private(set) var device = PairedDevice.load()
    /// The server's answer to GET /v1/status; nil until it answered once (or if too old).
    @Published private(set) var server: ServerStatus?
    @Published private(set) var reachable: Bool?
    /// The server no longer accepts this iPhone's token.
    @Published private(set) var revoked = false
    /// Host being set up from a link, while that runs.
    @Published private(set) var connecting: String?
    @Published var setupError: String?
    /// The Health sheet has types it never showed (first setup, or an update added some).
    @Published private(set) var needsHealthAccess = false
    @Published private(set) var askingHealth = false

    var isConfigured: Bool { settings.isComplete }

    /// A foreground sync runs on opening if the last one is older than this.
    static let syncAfter: TimeInterval = 15 * 60

    // MARK: Setup

    /// A setup link for a server other than the current one, until the user confirms it (see
    /// `confirm`) or cancels (sets it to nil).
    @Published var pendingLink: PairingLink?

    /// healthrelay://pair?server=…&code=…: pairs, asks for Health access, syncs. Any web page
    /// or message can open such a link, and pairing sends all Health history to the server it
    /// names, so a new server waits for the user to confirm it; re-pairing with the server
    /// that already has the data does not ask.
    func open(_ url: URL) async {
        guard let link = PairingLink(url) else {
            Log.error("setup: not a setup link: \(url.scheme ?? "")://\(url.host ?? "")")
            setupError = "This link is not a Health Relay setup link."
            return
        }
        if settings.sameServer(as: link.server.absoluteString) {
            await connect(with: link)
        } else {
            Log.info("setup: link for \(link.host), waiting for confirmation")
            setupError = nil
            pendingLink = link
        }
    }

    /// The user confirmed a setup link `open` held back.
    func confirm(_ link: PairingLink) async {
        if pendingLink == link { pendingLink = nil }
        await connect(with: link)
    }

    private func connect(with link: PairingLink) async {
        let host = link.host
        Log.info("setup: link for \(host)")
        await connect(to: link.server.absoluteString, host: host, token: nil) { old in
            try await ServerClient(base: link.server).pair(code: link.code, replacing: old)
        }
    }

    /// Server URL and token typed in by hand. The server token is exchanged for a device
    /// token when the server supports it; an older server keeps the token as given.
    func connectManually(url: String, token: String) async -> Bool {
        let typed = ServerSettings(url: url, token: token)
        guard typed.isComplete, let base = typed.baseURL else {
            setupError = "Enter the server's address (https://…) and its token."
            return false
        }
        await connect(to: base.absoluteString, host: typed.host, token: typed.token) { old in
            do {
                return try await ServerClient(base: base).pair(serverToken: typed.token, replacing: old)
            } catch ServerError.tooOld {
                return nil // a server older than pairing: the token as typed authorizes uploads
            } catch ServerError.unauthorized {
                // Not the server token: maybe a device token already. Keep it if it works.
                let status = try await ServerClient(base: base).status(token: typed.token)
                guard let device = status.device else { throw ServerError.unauthorized }
                return ServerClient.Paired(token: typed.token, device: device, server: status)
            }
        }
        return setupError == nil
    }

    /// Pairs with `pair` (given the current token when re-pairing with the same server), or
    /// keeps `token` when it returns nil.
    /// Setup changes (a link, manual entry, the server-token upgrade) one at a time, each
    /// starting from the token the previous one left. Run together, a link opened at launch
    /// and the upgrade both paired, and the device the overwritten token belonged to stayed
    /// on the server with a token nobody had.
    private var setupChain: Task<Void, Never>?

    private func exclusively(_ body: @escaping @MainActor () async -> Void) async {
        let previous = setupChain
        let task = Task { @MainActor in
            await previous?.value
            await body()
        }
        setupChain = task
        await task.value
    }

    private func connect(to url: String, host: String, token: String?,
                         pair: @escaping (String?) async throws -> ServerClient.Paired?) async {
        connecting = host
        setupError = nil
        defer { connecting = nil }
        var connected = false
        await exclusively { connected = await self.pairAndSave(url: url, host: host, token: token, pair: pair) }
        guard connected else { return }
        // Health access comes next (one tap on the main screen, which says what to pick);
        // with access already granted, syncing starts now.
        await refreshHealthAccess()
        if !needsHealthAccess { Task { await self.sync() } }
    }

    private func pairAndSave(url: String, host: String, token: String?,
                             pair: (String?) async throws -> ServerClient.Paired?) async -> Bool {
        let sameServer = settings.sameServer(as: url)
        do {
            let paired = try await pair(sameServer ? settings.token : nil)
            guard let newToken = paired?.token ?? token else { throw ServerError.tooOld }
            let new = ServerSettings(url: url, token: newToken)
            try new.save()
            if isConfigured && !sameServer {
                // Another server has none of this iPhone's history yet.
                await SyncEngine.shared.resetAnchors()
                Log.info("setup: new server, history will be sent again")
            }
            settings = new
            device = paired?.device
            PairedDevice.save(paired?.device)
            server = paired?.server
            reachable = true
            revoked = false
            SyncStatus.shared.lastError = nil
            Log.info("setup: connected to \(host)" + (paired.map { " as device \($0.device.id)" } ?? " with the server token"))
        } catch {
            Log.error("setup: \(host): \(error.localizedDescription)")
            setupError = error.localizedDescription
            return false
        }
        return true
    }

    /// Forgets the server (the token stays valid on the server until revoked there).
    func disconnect() {
        Log.info("setup: disconnected from \(settings.host)")
        ServerSettings.clear()
        PairedDevice.save(nil)
        settings = ServerSettings.load()
        device = nil
        server = nil
        reachable = nil
        revoked = false
        SyncStatus.shared.lastError = nil
    }

    /// An app set up by hand before pairing existed holds the server token: swap it for a
    /// device token of its own, once, without asking anything.
    private func upgradeServerToken() async {
        await exclusively { await self.upgradeServerTokenNow() }
    }

    private func upgradeServerTokenNow() async {
        // Checked once it is this change's turn: a link handled meanwhile already paired.
        guard isConfigured, device == nil, let base = settings.baseURL,
              !UserDefaults.standard.bool(forKey: "tokenUpgradeTried") else { return }
        do {
            let paired = try await ServerClient(base: base).pair(serverToken: settings.token, replacing: nil)
            let new = ServerSettings(url: settings.url, token: paired.token)
            try new.save()
            settings = new
            device = paired.device
            PairedDevice.save(paired.device)
            server = paired.server
            Log.info("setup: server token replaced by device token \(paired.device.id)")
        } catch ServerError.network(let reason) {
            Log.info("setup: token upgrade postponed: \(reason)")
            return // tried again next time
        } catch {
            Log.info("setup: keeping the token as entered: \(error.localizedDescription)")
        }
        UserDefaults.standard.set(true, forKey: "tokenUpgradeTried")
    }

    // MARK: Running

    /// The app came to the foreground: ask for Health access if an update brought new types,
    /// refresh the server's numbers and sync if it has been a while.
    func becameActive() async {
        guard isConfigured, connecting == nil else { return }
        await upgradeServerToken()
        await refreshHealthAccess()
        await refreshServer()
        let last = SyncStatus.shared.lastSuccess
        if !revoked, !needsHealthAccess, last == nil || Date.now.timeIntervalSince(last!) > Self.syncAfter {
            await sync()
        }
    }

    func refreshHealthAccess() async {
        guard SyncEngine.isAvailable else { return }
        needsHealthAccess = await SyncEngine.shared.needsAuthorization()
        if !needsHealthAccess { SyncStatus.shared.historyLimit = await SyncEngine.shared.historyLimit() }
    }

    /// The one Health sheet (HealthKit shows it only for types never asked about), then a sync.
    /// Started from the main screen, which first says what to pick: the sheet covers the app.
    func allowHealthAccess() async {
        guard SyncEngine.isAvailable, !askingHealth else { return }
        askingHealth = true
        do {
            try await SyncEngine.shared.requestAuthorization()
        } catch {
            Log.error("authorization: \(error.localizedDescription)")
            SyncStatus.shared.lastError = "Could not ask for Health access: \(error.localizedDescription)"
        }
        askingHealth = false
        await refreshHealthAccess()
        await sync()
    }

    func sync() async {
        guard isConfigured, !SyncStatus.shared.running, !askingHealth else { return }
        // A first sync can take long: keep the phone from locking (and HealthKit from
        // becoming unreadable) while the app is open.
        UIApplication.shared.isIdleTimerDisabled = true
        await SyncEngine.shared.syncAll()
        UIApplication.shared.isIdleTimerDisabled = false
        if SyncStatus.shared.lastError == UploadError.unauthorized.errorDescription { revoked = true }
        await refreshServer()
    }

    func refreshServer() async {
        guard let base = settings.baseURL else { return }
        do {
            server = try await ServerClient(base: base).status(token: settings.token)
            reachable = true
            revoked = false
        } catch ServerError.unauthorized {
            reachable = true
            revoked = true
        } catch ServerError.tooOld {
            reachable = true
        } catch {
            reachable = false
        }
    }

    #if DEBUG
    /// Tests change the stored settings behind the model's back.
    func reloadForTesting() {
        settings = ServerSettings.load()
        device = PairedDevice.load()
        server = nil
        connecting = nil
        setupError = nil
        pendingLink = nil
    }

    /// UI tests start from a fresh install: -hkr-reset-state.
    static func resetForTesting() {
        ServerSettings.clear()
        if let id = Bundle.main.bundleIdentifier { UserDefaults.standard.removePersistentDomain(forName: id) }
        try? FileManager.default.removeItem(at: FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0])
    }
    #endif
}
