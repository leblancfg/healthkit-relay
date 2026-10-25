import XCTest
@testable import HealthKitRelay

/// A fake healthkit-relay server, in process: answers pairing, status and erase calls,
/// slowly enough for concurrent calls to overlap, and keeps the requests it got.
final class StubServer: URLProtocol, @unchecked Sendable {
    struct Pair: Equatable {
        var code: String
        var replaces: String?
        var token: String
    }

    nonisolated(unsafe) static var pairs: [Pair] = []
    nonisolated(unsafe) static var requests: [URLRequest] = []
    /// Status and body of the answer to DELETE /v1/data.
    nonisolated(unsafe) static var eraseAnswer = (200, #"{"erased": 42, "device": "dev1"}"#)
    static let lock = NSLock()

    override class func canInit(with request: URLRequest) -> Bool { request.url?.host == "stub.test" }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func stopLoading() {}

    override func startLoading() {
        let path = request.url?.path ?? ""
        Self.lock.lock()
        Self.requests.append(request)
        let eraseAnswer = Self.eraseAnswer
        Self.lock.unlock()
        var body = request.httpBody ?? Data()
        if body.isEmpty, let stream = request.httpBodyStream {
            stream.open()
            var buf = [UInt8](repeating: 0, count: 4096)
            while stream.hasBytesAvailable {
                let n = stream.read(&buf, maxLength: buf.count)
                guard n > 0 else { break }
                body.append(buf, count: n)
            }
            stream.close()
        }
        let json = (try? JSONSerialization.jsonObject(with: body)) as? [String: String] ?? [:]
        var answer: [String: Any] = [:]
        var status = 200
        let server: [String: Any] = ["version": "test", "wire_version": 2, "records": 1, "server_id": "stub-server-id"]
        switch path {
        case "/v1/pairing-codes":
            answer = ["code": "UPGRADE"]
        case "/v1/pair":
            Self.lock.lock()
            let n = Self.pairs.count + 1
            let pair = Pair(code: json["code"] ?? "", replaces: json["replaces"], token: "device-token-\(n)")
            Self.pairs.append(pair)
            Self.lock.unlock()
            answer = ["token": pair.token, "device": ["id": "dev\(n)", "name": "iPhone"], "server": server]
        default:
            answer = server
        }
        var data = try! JSONSerialization.data(withJSONObject: answer)
        if path == "/v1/data" {
            (status, data) = (eraseAnswer.0, Data(eraseAnswer.1.utf8))
        }
        DispatchQueue.global().asyncAfter(deadline: .now() + 0.2) {
            self.client?.urlProtocol(self, didReceive: HTTPURLResponse(url: self.request.url!, statusCode: status, httpVersion: nil, headerFields: nil)!,
                                     cacheStoragePolicy: .notAllowed)
            self.client?.urlProtocol(self, didLoad: data)
            self.client?.urlProtocolDidFinishLoading(self)
        }
    }
}

@MainActor
final class AppModelTests: XCTestCase {
    private var saved: (ServerSettings, Data?, Bool)!

    override func setUp() async throws {
        saved = (ServerSettings.load(), UserDefaults.standard.data(forKey: "pairedDevice"),
                 UserDefaults.standard.bool(forKey: "tokenUpgradeTried"))
        URLProtocol.registerClass(StubServer.self)
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [StubServer.self]
        AddressHandoff.noRedirects = AddressHandoff.session(config)
        StubServer.pairs = []
        StubServer.requests = []
        StubServer.eraseAnswer = (200, #"{"erased": 42, "device": "dev1"}"#)
    }

    override func tearDown() async throws {
        URLProtocol.unregisterClass(StubServer.self)
        AddressHandoff.noRedirects = AddressHandoff.session()
        try? FileManager.default.removeItem(at: AnchorStore().directory.appendingPathComponent(marker + ".txt"))
        if saved.0.url.isEmpty { ServerSettings.clear() } else { try saved.0.save() }
        UserDefaults.standard.set(saved.1, forKey: "pairedDevice")
        UserDefaults.standard.set(saved.2, forKey: "tokenUpgradeTried")
        KnownServer.forget()
        AppModel.shared.reloadForTesting()
    }

    /// An app set up by hand (server token) and opened through a setup link: the launch-time
    /// token upgrade and the link's pairing must not both keep a device, or one is left on
    /// the server with a token nobody has (seen on a phone with 0.4.0).
    func testSetupLinkAtLaunchLeavesOneDevice() async throws {
        try ServerSettings(url: "https://stub.test", token: "server-token-0123456789").save()
        PairedDevice.save(nil)
        UserDefaults.standard.set(false, forKey: "tokenUpgradeTried")
        let model = AppModel.shared
        model.reloadForTesting()

        async let launched: Void = model.becameActive()
        async let opened: Void = model.open(URL(string: "healthrelay://pair?server=https%3A%2F%2Fstub.test&code=LINK")!)
        _ = await (launched, opened)

        let pairs = StubServer.pairs
        XCTAssertFalse(pairs.isEmpty)
        XCTAssertTrue(pairs.contains { $0.code == "LINK" }, "\(pairs)")
        // Every pairing after the first replaces the token the one before it got.
        for (previous, next) in zip(pairs, pairs.dropFirst()) {
            XCTAssertEqual(next.replaces, previous.token, "\(pairs)")
        }
        XCTAssertEqual(model.settings.token, pairs.last?.token)
        XCTAssertEqual(ServerSettings.load().token, pairs.last?.token)
        XCTAssertEqual(model.device?.id, "dev\(pairs.count)")
    }

    // MARK: Erase Server Data and Start Over

    private let marker = "erase-test-marker"

    /// Paired with the stub server, idle, with a sync position on disk.
    private func erasable() async throws -> AppModel {
        try ServerSettings(url: "https://stub.test", token: "device-token-erase").save()
        PairedDevice.save(PairedDevice(id: "dev1", name: "iPhone"))
        UserDefaults.standard.set(true, forKey: "tokenUpgradeTried")
        let model = AppModel.shared
        model.reloadForTesting()
        for _ in 0..<300 where SyncStatus.shared.running { try await Task.sleep(for: .milliseconds(100)) }
        try AnchorStore().setValue("2026-01-01", for: marker)
        return model
    }

    private var positionsKept: Bool { AnchorStore().value(for: marker) != nil }

    private var eraseRequests: [URLRequest] { StubServer.requests.filter { $0.url?.path == "/v1/data" } }

    func testEraseResetsAndSyncsAfterTheServerErased() async throws {
        let model = try await erasable()
        var synced = false
        try await model.eraseServerDataAndStartOver { synced = true }

        XCTAssertEqual(eraseRequests.count, 1)
        let request = try XCTUnwrap(eraseRequests.first)
        XCTAssertEqual(request.httpMethod, "DELETE")
        XCTAssertEqual(request.url?.absoluteString, "https://stub.test/v1/data?confirm=erase")
        XCTAssertEqual(request.value(forHTTPHeaderField: "Authorization"), "Bearer device-token-erase")
        XCTAssertEqual(request.timeoutInterval, 600)
        XCTAssertFalse(positionsKept, "sync positions not reset")
        XCTAssertTrue(synced, "no full sync started")
        XCTAssertFalse(model.erasing)
    }

    func testServerTooOldToEraseChangesNothing() async throws {
        for code in [404, 405] {
            let model = try await erasable()
            StubServer.requests = []
            StubServer.eraseAnswer = (code, "404 page not found")
            var synced = false
            do {
                try await model.eraseServerDataAndStartOver { synced = true }
                XCTFail("HTTP \(code): no error")
            } catch {
                XCTAssertEqual(error as? ServerError, .eraseUnsupported, "HTTP \(code)")
                XCTAssertTrue(error.localizedDescription.contains("too old to erase"), error.localizedDescription)
            }
            XCTAssertEqual(eraseRequests.count, 1, "HTTP \(code)")
            XCTAssertTrue(positionsKept, "HTTP \(code): sync positions reset")
            XCTAssertFalse(synced, "HTTP \(code): sync started")
            XCTAssertFalse(model.erasing)
        }
    }

    func testFailedEraseChangesNothing() async throws {
        let answers: [(Int, String, ServerError)] = [
            (500, #"{"error": "disk full"}"#, .http(500, "disk full")),
            (401, #"{"error": "unauthorized"}"#, .unauthorized),
            (200, "<html>a proxy</html>", .http(200, "unexpected answer: <html>a proxy</html>")),
        ]
        for (code, body, expected) in answers {
            let model = try await erasable()
            StubServer.eraseAnswer = (code, body)
            var synced = false
            do {
                try await model.eraseServerDataAndStartOver { synced = true }
                XCTFail("HTTP \(code): no error")
            } catch {
                XCTAssertEqual(error as? ServerError, expected, "HTTP \(code)")
            }
            XCTAssertTrue(positionsKept, "HTTP \(code): sync positions reset")
            XCTAssertFalse(synced, "HTTP \(code): sync started")
            XCTAssertFalse(model.erasing)
        }
    }

    func testUnreachableServerChangesNothing() async throws {
        let model = try await erasable()
        try ServerSettings(url: "https://unreachable.invalid", token: "device-token-erase").save()
        model.reloadForTesting()
        var synced = false
        do {
            try await model.eraseServerDataAndStartOver { synced = true }
            XCTFail("no error")
        } catch {
            guard case .network = error as? ServerError else { return XCTFail("\(error)") }
        }
        XCTAssertTrue(positionsKept)
        XCTAssertFalse(synced)
    }

    /// Any web page can open a setup link: one for a server other than the current one pairs
    /// (and sends Health data there) only once the user confirms, and never gets the old token.
    func testLinkToAnotherServerWaitsForConfirmation() async throws {
        try ServerSettings(url: "https://mine.invalid", token: "device-token-mine").save()
        PairedDevice.save(PairedDevice(id: "mine", name: "iPhone"))
        let model = AppModel.shared
        model.reloadForTesting()
        let url = URL(string: "healthrelay://pair?server=https%3A%2F%2Fstub.test&code=LINK")!

        await model.open(url)
        XCTAssertEqual(StubServer.pairs, [])
        XCTAssertEqual(model.pendingLink?.host, "stub.test")
        XCTAssertEqual(ServerSettings.load().url, "https://mine.invalid")

        model.pendingLink = nil // Cancel
        XCTAssertEqual(StubServer.pairs, [])
        XCTAssertEqual(model.settings.token, "device-token-mine")

        await model.open(url)
        let link = try XCTUnwrap(model.pendingLink)
        await model.confirm(link)
        XCTAssertNil(model.pendingLink)
        XCTAssertEqual(StubServer.pairs, [StubServer.Pair(code: "LINK", replaces: nil, token: "device-token-1")])
        XCTAssertEqual(ServerSettings.load().url, "https://stub.test")
        XCTAssertEqual(model.settings.token, "device-token-1")
    }

    /// A setup link for the address the paired server named as its preferred one: no
    /// confirmation, the same device on the server, sync positions kept. A link for any other
    /// address waits for confirmation, whatever that address answers.
    func testLinkToTheServersPreferredAddressKeepsTheDevice() async throws {
        let model = try await erasable()
        // Named by another connection's server: does not count.
        KnownServer.preferredURL = "https://stub.test:8443"
        KnownServer.preferredFor = "https://elsewhere.test"
        defer { KnownServer.forget() }
        await model.open(URL(string: "healthrelay://pair?server=https%3A%2F%2Fstub.test%3A8443&code=NOTYET")!)
        XCTAssertNotNil(model.pendingLink, "a preferred address noted for another connection skipped the question")
        model.pendingLink = nil
        // Named by this server, reached at the current address.
        KnownServer.preferredFor = "https://stub.test"
        await model.open(URL(string: "healthrelay://pair?server=https%3A%2F%2Fstub.test%3A8443&code=MOVE")!)
        XCTAssertNil(model.pendingLink, "asked to confirm the same server")
        let pair = try XCTUnwrap(StubServer.pairs.last)
        XCTAssertEqual(pair.code, "MOVE")
        XCTAssertEqual(pair.replaces, "device-token-erase")
        XCTAssertEqual(model.settings.url, "https://stub.test:8443")
        XCTAssertTrue(positionsKept)

        // The stub answers with the same server ID at every address: still another address.
        await model.open(URL(string: "healthrelay://pair?server=https%3A%2F%2Fstub.test%3A9443&code=OTHER")!)
        XCTAssertNotNil(model.pendingLink)
        XCTAssertFalse(StubServer.pairs.contains { $0.code == "OTHER" })
        model.pendingLink = nil
    }

    /// Erase Server Data and Disconnect: stops, erases, disconnects. When the erase fails, the
    /// connection stays; when the user disconnected meanwhile, a late failure reconnects nothing.
    /// Two setups for the paired server, the second waiting behind the first, then a
    /// disconnect while the first awaits its answer: neither connects afterwards.
    func testDisconnectCancelsQueuedSetups() async throws {
        let model = try await erasable()
        async let first: Void = model.open(URL(string: "healthrelay://pair?server=https%3A%2F%2Fstub.test&code=ONE")!)
        async let second: Void = model.open(URL(string: "healthrelay://pair?server=https%3A%2F%2Fstub.test&code=TWO")!)
        try await Task.sleep(for: .milliseconds(60))
        model.disconnect()
        _ = await (first, second)
        XCTAssertEqual(ServerSettings.load().url, "", "a setup queued before the disconnect connected")
        XCTAssertFalse(model.isConfigured)
        XCTAssertFalse(StubServer.pairs.contains { $0.code == "TWO" }, "the queued setup still paired")
    }

    /// A setup link opened while Erase Server Data and Disconnect runs pairs nothing, and the
    /// erase still disconnects.
    func testSetupDuringEraseIsRefused() async throws {
        let model = try await erasable()
        let erase = Task { try await model.eraseServerDataAndDisconnect() }
        try await Task.sleep(for: .milliseconds(50))
        await model.open(URL(string: "healthrelay://pair?server=https%3A%2F%2Fstub.test&code=DURING")!)
        try await erase.value
        XCTAssertFalse(StubServer.pairs.contains { $0.code == "DURING" }, "paired during the erase")
        XCTAssertNotNil(model.setupError)
        XCTAssertFalse(model.isConfigured)
        XCTAssertEqual(ServerSettings.load().url, "")
    }

    /// An app still holding the admin token (set up by hand before pairing) does not swap it
    /// for a device token while Erase Server Data and Disconnect runs.
    func testNoTokenUpgradeDuringErase() async throws {
        let model = try await erasable()
        try ServerSettings(url: "https://stub.test", token: "server-token-0123456789").save()
        PairedDevice.save(nil)
        UserDefaults.standard.set(false, forKey: "tokenUpgradeTried")
        model.reloadForTesting()
        let erase = Task { try await model.eraseServerDataAndDisconnect() }
        try await Task.sleep(for: .milliseconds(50))
        await model.becameActive()
        try await erase.value
        XCTAssertEqual(StubServer.pairs, [], "the token upgrade paired during the erase")
        XCTAssertFalse(model.isConfigured)
        XCTAssertEqual(ServerSettings.load().url, "")
    }

    /// While a pairing awaits its answer (the server may already have rotated the token), the
    /// app neither moves to a preferred address nor notes one.
    func testNoAddressMoveWhileASetupRuns() async throws {
        let model = try await erasable()
        let old = model.settings
        async let setup: Void = model.open(URL(string: "healthrelay://pair?server=https%3A%2F%2Fstub.test&code=ROTATE")!)
        try await Task.sleep(for: .milliseconds(50))
        XCTAssertFalse(model.adoptAddress(ServerSettings(url: "https://stub.test:8443", token: old.token), from: old))
        XCTAssertFalse(model.noteHandoff(Handoff(serverID: "x", preferredURL: "https://other.test"), from: old))
        await setup
        XCTAssertNil(KnownServer.preferredURL.flatMap { $0 == "https://other.test" ? $0 : nil })
        XCTAssertEqual(model.settings.url, "https://stub.test")
        XCTAssertEqual(model.settings.token, StubServer.pairs.last?.token)
        XCTAssertEqual(ServerSettings.load(), model.settings)
    }

    func testEraseAndDisconnect() async throws {
        var model = try await erasable()
        try await model.eraseServerDataAndDisconnect()
        XCTAssertFalse(model.isConfigured)
        XCTAssertEqual(ServerSettings.load().url, "")
        XCTAssertEqual(eraseRequests.count, 1)
        // The server revokes the token in the same step: nothing sent before arrives after.
        XCTAssertEqual(eraseRequests.first?.url?.absoluteString, "https://stub.test/v1/data?confirm=erase&disconnect=1")

        model = try await erasable()
        StubServer.eraseAnswer = (500, #"{"error":"disk full"}"#)
        do {
            try await model.eraseServerDataAndDisconnect()
            XCTFail("erase succeeded")
        } catch {}
        XCTAssertEqual(ServerSettings.load().url, "https://stub.test", "a failed erase keeps the connection")

        // The answer (200 ms away) comes after a disconnect: nothing is restored.
        let erase = Task { try await model.eraseServerDataAndDisconnect() }
        try await Task.sleep(for: .milliseconds(50))
        model.disconnect()
        _ = try? await erase.value
        XCTAssertEqual(ServerSettings.load().url, "", "a late erase failure reconnected the server")
        XCTAssertFalse(model.isConfigured)
    }

    func testUnencryptedLinkIsFlagged() {
        XCTAssertTrue(PairingLink(URL(string: "healthrelay://pair?server=http%3A%2F%2F192.168.1.20%3A8080&code=C")!)!.isUnencrypted)
        XCTAssertFalse(PairingLink(URL(string: "healthrelay://pair?server=https%3A%2F%2Fh.example&code=C")!)!.isUnencrypted)
    }
}
