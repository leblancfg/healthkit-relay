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
        let server: [String: Any] = ["version": "test", "wire_version": 2, "records": 1]
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
        StubServer.pairs = []
        StubServer.requests = []
        StubServer.eraseAnswer = (200, #"{"erased": 42, "device": "dev1"}"#)
    }

    override func tearDown() async throws {
        URLProtocol.unregisterClass(StubServer.self)
        try? FileManager.default.removeItem(at: AnchorStore().directory.appendingPathComponent(marker + ".txt"))
        if saved.0.url.isEmpty { ServerSettings.clear() } else { try saved.0.save() }
        UserDefaults.standard.set(saved.1, forKey: "pairedDevice")
        UserDefaults.standard.set(saved.2, forKey: "tokenUpgradeTried")
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
}
