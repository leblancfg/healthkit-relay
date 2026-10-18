import XCTest
@testable import HealthKitRelay

/// A fake healthkit-relay server, in process: answers pairing and status calls, slowly
/// enough for concurrent calls to overlap.
final class StubServer: URLProtocol, @unchecked Sendable {
    struct Pair: Equatable {
        var code: String
        var replaces: String?
        var token: String
    }

    nonisolated(unsafe) static var pairs: [Pair] = []
    static let lock = NSLock()

    override class func canInit(with request: URLRequest) -> Bool { request.url?.host == "stub.test" }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func stopLoading() {}

    override func startLoading() {
        let path = request.url?.path ?? ""
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
        let data = try! JSONSerialization.data(withJSONObject: answer)
        DispatchQueue.global().asyncAfter(deadline: .now() + 0.2) {
            self.client?.urlProtocol(self, didReceive: HTTPURLResponse(url: self.request.url!, statusCode: 200, httpVersion: nil, headerFields: nil)!,
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
    }

    override func tearDown() async throws {
        URLProtocol.unregisterClass(StubServer.self)
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

    func testUnencryptedLinkIsFlagged() {
        XCTAssertTrue(PairingLink(URL(string: "healthrelay://pair?server=http%3A%2F%2F192.168.1.20%3A8080&code=C")!)!.isUnencrypted)
        XCTAssertFalse(PairingLink(URL(string: "healthrelay://pair?server=https%3A%2F%2Fh.example&code=C")!)!.isUnencrypted)
    }
}
