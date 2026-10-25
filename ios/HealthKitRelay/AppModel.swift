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

    /// Changes at every connect, disconnect and erase. An operation that awaited saves or
    /// restores settings only if it did not change meanwhile: a late answer never reconnects
    /// a server the user left.
    private var generation = 0

    /// Changes only at a disconnect or an erase. A setup accepted before it (waiting in
    /// `setupChain`, or awaiting an answer) does nothing after it. Unlike `generation`, a
    /// pairing does not change it: the setup queued after a pairing still runs, with the
    /// token that pairing left.
    private var lifecycle = 0

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
            setupError = "Enter the server address, starting with https://, for example https://health.example.ts.net, or http:// for an address on your local network, for example http://192.168.1.10:8080. Then enter the setup code from the server's setup page."
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
    /// Setups running or waiting their turn. Erasing, and moving to the server's preferred
    /// address, wait until there are none: a pairing may have rotated the token on the server.
    private var setupsRunning = 0

    private func exclusively(_ body: @escaping @MainActor () async -> Void) async {
        setupsRunning += 1
        defer { setupsRunning -= 1 }
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
        // An erase and a setup never overlap: the erase would disconnect the new pairing, or
        // the pairing undo the disconnect the erase promised.
        guard !erasing else {
            Log.info("setup: \(host) asked for while erasing; not connected")
            setupError = "Health Relay is erasing this iPhone's data on the server. Open the setup link again once that is done."
            return
        }
        connecting = host
        setupError = nil
        defer { connecting = nil }
        var connected = false
        let accepted = lifecycle
        await exclusively {
            connected = await self.pairAndSave(url: url, host: host, token: token, accepted: accepted, pair: pair)
        }
        guard connected else { return }
        // Health access comes next (one tap on the main screen, which says what to pick);
        // with access already granted, syncing starts now.
        await refreshHealthAccess()
        if !needsHealthAccess { Task { await self.sync(userInitiated: true) } }
    }

    private func pairAndSave(url: String, host: String, token: String?, accepted: Int,
                             pair: (String?) async throws -> ServerClient.Paired?) async -> Bool {
        guard lifecycle == accepted, !erasing else {
            Log.info("setup: \(host) was asked for before a disconnect or an erase; not connected")
            return false
        }
        let sameServer = URL(string: url).map { KnownServer.isCurrent($0, settings: settings) } ?? false
        let started = generation
        do {
            let paired = try await pair(sameServer ? settings.token : nil)
            guard generation == started, lifecycle == accepted else {
                Log.info("setup: \(host) answered after a disconnect or another setup; not connected")
                return false
            }
            guard let newToken = paired?.token ?? token else { throw ServerError.tooOld }
            let new = ServerSettings(url: url, token: newToken)
            if !sameServer {
                // What still runs for the old connection stops before its positions are reset.
                SyncEngine.shared.backgroundTimeOver(force: true)
                BackgroundUploads.shared.cancelAll()
            }
            generation += 1
            let mine = generation
            try new.save()
            if !sameServer {
                // Another server (or a new pairing after a disconnect) has none of this
                // iPhone's history yet.
                await SyncEngine.shared.resetAnchors()
                Log.info("setup: new server, history will be sent again")
                // A disconnect during the reset cleared the settings: this setup is over.
                guard generation == mine, lifecycle == accepted else { return false }
            }
            settings = new
            device = paired?.device
            PairedDevice.save(paired?.device)
            server = paired?.server
            if !sameServer { KnownServer.forget() }
            if let status = paired?.server { KnownServer.note(status.handoff, at: new) }
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

    /// The server's preferred address answered as the same server (AddressHandoff, from a
    /// status check or a heartbeat): the app moves there, history and sync positions kept.
    /// One step on the main actor, like disconnect and pairing: it saves only if the app is
    /// still connected with `old`, as the app and as stored.
    /// Not while a setup or an erase runs: a pairing may have rotated the token `old` holds.
    @discardableResult
    func adoptAddress(_ new: ServerSettings, from old: ServerSettings) -> Bool {
        guard setupsRunning == 0, !erasing, settings == old, ServerSettings.load() == old,
              (try? new.save()) != nil else { return false }
        generation += 1
        settings = new
        Log.info("server address: moved from \(old.host) to \(new.host), as the server asked")
        return true
    }

    /// What the paired server, reached with `old`, said about itself (a heartbeat or status
    /// answer): noted if the app is still connected with `old` and no setup or erase runs.
    /// Returns whether it was, and the preferred address may be followed.
    func noteHandoff(_ handoff: Handoff, from old: ServerSettings) -> Bool {
        guard setupsRunning == 0, !erasing, settings == old, ServerSettings.load() == old else { return false }
        KnownServer.note(handoff, at: old)
        return true
    }

    /// Refused while a setup runs or waits (see `setupsRunning`).
    private func checkNoSetup() throws {
        guard setupsRunning == 0, connecting == nil else {
            throw ServerError.notAllowed("A setup is running: erase once it is done.")
        }
    }

    /// Has the server permanently delete everything this iPhone uploaded, then disconnects:
    /// nothing is sent again. Syncing and uploads stop first, so nothing arrives after the
    /// erase. Throws, with the connection as it was, unless the server erased.
    func eraseServerDataAndDisconnect() async throws {
        guard !erasing else { return }
        guard isConfigured, let base = settings.baseURL else { throw UploadError.notConfigured }
        try checkNoSetup()
        let kept = settings
        erasing = true
        defer { erasing = false }
        generation += 1
        lifecycle += 1
        let started = generation
        Log.info("erase: stopping, then asking \(kept.host) to delete this iPhone's data")
        ServerSettings.clear()
        SyncEngine.shared.backgroundTimeOver(force: true)
        await BackgroundUploads.shared.cancelAllAndWait()
        do {
            let erased = try await ServerClient(base: base).erase(token: kept.token)
            Log.info("erase: server deleted \(erased.erased) items; disconnecting")
        } catch {
            // Still connected as before, unless the user disconnected or set up another
            // server while the erase was on its way.
            if generation == started { try? kept.save() }
            Log.error("erase: \(error.localizedDescription)" + (generation == started ? "; still connected" : ""))
            throw error
        }
        if generation == started { disconnect() }
    }

    /// Forgets the server (the token stays valid on the server until revoked there) and stops
    /// what was under way: the running sync and the uploads the system carries on.
    func disconnect() {
        generation += 1
        lifecycle += 1
        Log.info("setup: disconnected from \(settings.host)")
        ServerSettings.clear()
        SyncEngine.shared.backgroundTimeOver(force: true)
        BackgroundUploads.shared.cancelAll()
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
        let accepted = lifecycle
        await exclusively { if self.lifecycle == accepted { await self.upgradeServerTokenNow() } }
    }

    private func upgradeServerTokenNow() async {
        // Checked once it is this change's turn: a link handled meanwhile already paired.
        guard isConfigured, device == nil, let base = settings.baseURL,
              !UserDefaults.standard.bool(forKey: "tokenUpgradeTried") else { return }
        let started = generation
        do {
            let paired = try await ServerClient(base: base).pair(serverToken: settings.token, replacing: nil)
            guard generation == started else { return }
            generation += 1
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
        guard isConfigured, !askingHealth, !erasing else { return }
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
        try checkNoSetup()
        erasing = true
        let accepted = lifecycle
        let token = settings.token
        // Uploads the system still carries could reach the server after the erase.
        await BackgroundUploads.shared.cancelAllAndWait()
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
        // Disconnected meanwhile: nothing is sent again.
        guard lifecycle == accepted, isConfigured else { return }
        Log.info("erase: sync positions reset, sending all history")
        await refreshServer()
        if let thenSync { await thenSync() } else { await sync(userInitiated: true) }
    }

    func refreshServer() async {
        let asked = settings
        guard let base = asked.baseURL else { return }
        do {
            let status = try await ServerClient(base: base).status(token: asked.token)
            // A setup or disconnect while the answer was on its way: it is not this server's.
            guard settings == asked else { return }
            server = status
            reachable = true
            revoked = false
            if noteHandoff(status.handoff, from: asked),
               let moved = await AddressHandoff.follow(status.handoff, from: asked, device: device?.id) {
                adoptAddress(moved, from: asked)
            }
        } catch ServerError.unauthorized {
            guard settings == asked else { return }
            reachable = true
            revoked = true
        } catch ServerError.tooOld {
            guard settings == asked else { return }
            reachable = true
        } catch {
            guard settings == asked else { return }
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

