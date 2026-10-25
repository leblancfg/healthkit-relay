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
    /// The server is deleting this iPhone's data (Erase Server Data and Start Over).
    @Published private(set) var erasing = false

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
            setupError = "This link is not a setup code from a healthkit-relay server. Open your server's setup page and scan the setup code there."
            return
        }
        // The server already paired, maybe at the other address it named itself: no question,
        // and the device and its history stay.
        if KnownServer.isCurrent(link.server, settings: settings) {
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

    /// A setup code as shown on the setup page (24 characters of A-Z and 2-7, dashes and spaces
    /// aside); admin tokens are longer and use other characters.
    nonisolated static func isSetupCode(_ text: String) -> Bool {
        let code = text.uppercased().filter { $0 != "-" && !$0.isWhitespace }
        return (16...32).contains(code.count) && code.allSatisfy { ("A"..."Z").contains($0) || ("2"..."7").contains($0) }
    }

    /// Server address and a setup code or the admin token, typed in by hand. A setup code pairs
    /// like a scanned one. The admin token is exchanged for a device token when the server
    /// supports it; an older server keeps the token as given.
    func connectManually(url: String, token: String) async -> Bool {
        let typed = ServerSettings(url: url, token: token)
        guard typed.isComplete, let base = typed.baseURL else {
            setupError = "Enter the server address, starting with http:// or https://, for example http://192.168.1.10:8080. Then enter the setup code from the server's setup page."
            return false
        }
        if Self.isSetupCode(typed.token) {
            Log.info("setup: setup code typed in for \(typed.host)")
            await connect(to: base.absoluteString, host: typed.host, token: nil) { old in
                try await ServerClient(base: base).pair(code: typed.token.trimmingCharacters(in: .whitespacesAndNewlines), replacing: old)
            }
            return setupError == nil
        }
        await connect(to: base.absoluteString, host: typed.host, token: typed.token) { old in
            do {
                return try await ServerClient(base: base).pair(serverToken: typed.token, replacing: old)
            } catch ServerError.tooOld {
                return nil // a server older than pairing: the token as typed authorizes uploads
            } catch ServerError.unauthorized {
                // Not the admin token: maybe a paired device token already. Keep it if it works.
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
        if !needsHealthAccess { Task { await self.sync(userInitiated: true) } }
    }

    private func pairAndSave(url: String, host: String, token: String?,
                             pair: (String?) async throws -> ServerClient.Paired?) async -> Bool {
        let sameServer = URL(string: url).map { KnownServer.isCurrent($0, settings: settings) } ?? false
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
            if !sameServer { KnownServer.forget() }
            if let status = paired?.server { KnownServer.note(status.handoff) }
            reachable = true
            revoked = false
            SyncStatus.shared.lastError = nil
            Log.info("setup: connected to \(host)" + (paired.map { " as device \($0.device.id)" } ?? " with the admin token"))
        } catch {
            Log.error("setup: \(host): \(error.localizedDescription)")
            setupError = error.localizedDescription
            return false
        }
        return true
    }

    /// The server's preferred address answered as the same server (AddressHandoff), from a
    /// status check or a heartbeat: settings already saved, history and sync positions kept.
    func serverMoved(to new: ServerSettings) {
        guard ServerSettings.load() == new else { return }
        settings = new
    }

    /// Forgets the server (the token stays valid on the server until revoked there).
    func disconnect() {
        Log.info("setup: disconnected from \(settings.host)")
        ServerSettings.clear()
        PairedDevice.save(nil)
        KnownServer.forget()
        settings = ServerSettings.load()
        device = nil
        server = nil
        reachable = nil
        revoked = false
        SyncStatus.shared.lastError = nil
    }

    /// An app set up by hand before pairing existed holds the admin token: swap it for a
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
            Log.info("setup: admin token replaced by the paired device token \(paired.device.id)")
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
        // What a locked launch could not read from HealthKit is read now, and a sync owed
        // (skipped while locked, or failed) runs, whenever the last one was.
        let owed = await SyncEngine.shared.pending != nil
        await SyncEngine.shared.startObserving()
        let last = SyncStatus.shared.lastSuccess
        if !revoked, !needsHealthAccess, owed || last == nil || Date.now.timeIntervalSince(last!) > Self.syncAfter {
            await sync()
        }
    }

    /// A sync likely to take minutes: the first, one after the positions were reset, or one
    /// after `Self.longAfter` without a sync.
    static let longAfter: TimeInterval = 6 * 60 * 60

    private func isLongSync() async -> Bool {
        guard let last = SyncStatus.shared.lastSuccess, await SyncEngine.shared.hasHistory else { return true }
        return Date.now.timeIntervalSince(last) > Self.longAfter
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
        await sync(userInitiated: true)
    }

    /// Syncs, or joins the sync running (a HealthKit wake, an unlock): one sync on screen.
    /// `userInitiated`: a tap (Sync Now, pulling down, Allow Health access). A long sync the
    /// user started may go on after they leave the app (iOS 26, see ContinuedSync).
    func sync(userInitiated: Bool = false) async {
        guard isConfigured, !askingHealth else { return }
        // A first sync can take long: keep the phone from locking (and HealthKit from
        // becoming unreadable) while the app is open.
        UIApplication.shared.isIdleTimerDisabled = true
        let work = Task { await SyncEngine.shared.syncAll() }
        if userInitiated, #available(iOS 26.0, *), await isLongSync() { ContinuedSync.start() }
        await work.value
        UIApplication.shared.isIdleTimerDisabled = false
        if SyncStatus.shared.lastError == UploadError.unauthorized.errorDescription { revoked = true }
        await refreshServer()
    }

    /// Has the server permanently delete everything this iPhone uploaded, then sends all
    /// Health data again from scratch. Throws, with nothing reset, unless the server erased
    /// (HTTP 200); a server too old to erase throws ServerError.eraseUnsupported.
    /// `thenSync` replaces the full sync in tests.
    func eraseServerDataAndStartOver(thenSync: (@MainActor () async -> Void)? = nil) async throws {
        guard !erasing else { return }
        guard isConfigured, let base = settings.baseURL else { throw UploadError.notConfigured }
        guard !SyncStatus.shared.running else { throw ServerError.notAllowed("A sync is running: erase once it is done.") }
        erasing = true
        let token = settings.token
        Log.info("erase: asking \(settings.host) to delete this iPhone's data")
        do {
            try await SyncEngine.shared.resetAnchors {
                let erased = try await ServerClient(base: base).erase(token: token)
                Log.info("erase: server deleted \(erased.erased) items of device \(erased.device.flatMap { $0.isEmpty ? nil : $0 } ?? "(admin token)")")
            }
        } catch {
            erasing = false
            Log.error("erase: \(error.localizedDescription)")
            throw error
        }
        erasing = false
        Log.info("erase: sync positions reset, sending all history")
        await refreshServer()
        if let thenSync { await thenSync() } else { await sync(userInitiated: true) }
    }

    func refreshServer() async {
        guard let base = settings.baseURL else { return }
        do {
            let status = try await ServerClient(base: base).status(token: settings.token)
            server = status
            KnownServer.note(status.handoff)
            reachable = true
            revoked = false
            if connecting == nil, let moved = await AddressHandoff.follow(status.handoff, from: settings, device: device?.id) {
                serverMoved(to: moved)
            }
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

