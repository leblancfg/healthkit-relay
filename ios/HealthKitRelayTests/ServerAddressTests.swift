import XCTest
@testable import HealthKitRelay

/// The server's current and preferred addresses, in process. `answers` maps host + path to a
/// status code and JSON body; a missing entry is a 404.
final class HandoffStub: URLProtocol, @unchecked Sendable {
    nonisolated(unsafe) static var answers: [String: (Int, String)] = [:]
    nonisolated(unsafe) static var requests: [URLRequest] = []
    static let lock = NSLock()

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func stopLoading() {}

    override func startLoading() {
        let key = (request.url?.host ?? "") + (request.url?.path ?? "")
        Self.lock.lock()
        Self.requests.append(request)
        let (status, body) = Self.answers[key] ?? (404, #"{"error":"not found"}"#)
        Self.lock.unlock()
        var headers: [String: String] = [:]
        if (300..<400).contains(status) { headers["Location"] = body }
        let response = HTTPURLResponse(url: request.url!, statusCode: status, httpVersion: nil, headerFields: headers)!
        if (300..<400).contains(status), let to = URL(string: body) {
            client?.urlProtocol(self, wasRedirectedTo: URLRequest(url: to), redirectResponse: response)
        }
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data(body.utf8))
        client?.urlProtocolDidFinishLoading(self)
    }
}

final class ServerAddressTests: XCTestCase {
    private var saved: ServerSettings!
    private var defaults: UserDefaults!
    private let current = ServerSettings(url: "https://lump.example.ts.net:8180", token: "device-token-handoff")
    private let id = "5f0c2a9e5f0c2a9e5f0c2a9e5f0c2a9e"
    private let handoff = Handoff(serverID: "5f0c2a9e5f0c2a9e5f0c2a9e5f0c2a9e", preferredURL: "https://health.example.ts.net")

    private var session: URLSession {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [HandoffStub.self]
        return AddressHandoff.session(config)
    }

    override func setUp() async throws {
        saved = ServerSettings.load()
        try current.save()
        defaults = UserDefaults(suiteName: "ServerAddressTests")
        defaults.removePersistentDomain(forName: "ServerAddressTests")
        HandoffStub.requests = []
        HandoffStub.answers = [
            "health.example.ts.net/v1/server": (200, #"{"server_id":"\#(id)","wire_version":2}"#),
            "health.example.ts.net/v1/status": (200, #"{"version":"1.0.0","wire_version":2,"records":7,"device":{"id":"dev1","name":"iPhone"},"server_id":"\#(id)","preferred_url":"https://health.example.ts.net"}"#),
        ]
    }

    override func tearDown() async throws {
        if saved.url.isEmpty { ServerSettings.clear() } else { try saved.save() }
    }

    private func follow(_ h: Handoff? = nil, now: Date = .now) async -> ServerSettings? {
        await AddressHandoff.follow(h ?? handoff, from: current, device: "dev1", session: session, defaults: defaults, now: now)
    }

    /// Requests that carried the token to the preferred address.
    private var tokenSentToNewAddress: Bool {
        HandoffStub.requests.contains { $0.url?.host == "health.example.ts.net" && $0.value(forHTTPHeaderField: "Authorization") != nil }
    }

    func testMovesToThePreferredAddress() async throws {
        let result = await follow()
        let moved = try XCTUnwrap(result)
        XCTAssertEqual(moved.url, "https://health.example.ts.net")
        XCTAssertEqual(moved.token, current.token)
        XCTAssertEqual(ServerSettings.load(), current, "follow saves nothing")
        let adopted = await MainActor.run { () -> Bool in
            AppModel.shared.reloadForTesting()
            return AppModel.shared.adoptAddress(moved, from: current)
        }
        XCTAssertTrue(adopted)
        XCTAssertEqual(ServerSettings.load(), moved)
        // The identity check goes first, without the token.
        let first = try XCTUnwrap(HandoffStub.requests.first)
        XCTAssertEqual(first.url?.path, "/v1/server")
        XCTAssertNil(first.value(forHTTPHeaderField: "Authorization"))
        // Once there, nothing more to do.
        let r1 = await AddressHandoff.follow(handoff, from: moved, device: "dev1", session: session, defaults: defaults)
        XCTAssertNil(r1)
    }

    /// Disconnected between the checks and the move: nothing is written back.
    func testDisconnectBeforeTheMoveWins() async throws {
        let result = await follow()
        let moved = try XCTUnwrap(result)
        let adopted = await MainActor.run { () -> Bool in
            AppModel.shared.reloadForTesting()
            AppModel.shared.disconnect()
            return AppModel.shared.adoptAddress(moved, from: current)
        }
        XCTAssertFalse(adopted)
        XCTAssertEqual(ServerSettings.load().url, "")
    }

    func testStaysWhenAnotherServerAnswers() async {
        HandoffStub.answers["health.example.ts.net/v1/server"] = (200, #"{"server_id":"0000000000000000ffffffffffffffff"}"#)
        let r2 = await follow()
        XCTAssertNil(r2)
        XCTAssertEqual(ServerSettings.load(), current)
        XCTAssertFalse(tokenSentToNewAddress)
    }

    func testStaysWhenTheTokenIsAnotherDevices() async {
        HandoffStub.answers["health.example.ts.net/v1/status"] = (200, #"{"version":"1.0.0","wire_version":2,"records":7,"device":{"id":"dev2","name":"iPhone"},"server_id":"\#(id)"}"#)
        let r3 = await follow()
        XCTAssertNil(r3)
        XCTAssertEqual(ServerSettings.load(), current)
    }

    func testStaysWhenTheTokenIsRefused() async {
        HandoffStub.answers["health.example.ts.net/v1/status"] = (401, #"{"error":"unauthorized"}"#)
        let r4 = await follow()
        XCTAssertNil(r4)
        XCTAssertEqual(ServerSettings.load(), current)
    }

    func testOnlyHTTPS() async {
        let r5 = await follow(Handoff(serverID: id, preferredURL: "http://192.168.1.10:8080"))
        XCTAssertNil(r5)
        XCTAssertTrue(HandoffStub.requests.isEmpty)
        let r6 = await follow(Handoff(serverID: nil, preferredURL: "https://health.example.ts.net"))
        XCTAssertNil(r6)
        XCTAssertTrue(HandoffStub.requests.isEmpty)
    }

    func testRedirectsAreNotFollowed() async {
        HandoffStub.answers["health.example.ts.net/v1/server"] = (302, "https://elsewhere.example/v1/server")
        HandoffStub.answers["elsewhere.example/v1/server"] = (200, #"{"server_id":"\#(id)"}"#)
        let r7 = await follow()
        XCTAssertNil(r7)
        XCTAssertFalse(HandoffStub.requests.contains { $0.url?.host == "elsewhere.example" })
        XCTAssertEqual(ServerSettings.load(), current)
    }

    func testFailedAddressIsTriedAgainLater() async {
        HandoffStub.answers["health.example.ts.net/v1/server"] = (502, "bad gateway")
        let start = Date.now
        let r8 = await follow(now: start)
        XCTAssertNil(r8)
        let tries = HandoffStub.requests.count
        let r9 = await follow(now: start.addingTimeInterval(60))
        XCTAssertNil(r9)
        XCTAssertEqual(HandoffStub.requests.count, tries, "tried again within the hour")
        HandoffStub.answers["health.example.ts.net/v1/server"] = (200, #"{"server_id":"\#(id)"}"#)
        let r10 = await follow(now: start.addingTimeInterval(AddressHandoff.retryAfter + 1))
        XCTAssertNotNil(r10)
    }

    func testANewSetupMeanwhileWins() async throws {
        // Another pairing saved new settings while the check ran: the handoff gives up.
        try ServerSettings(url: "https://other.example", token: "t2").save()
        let r11 = await follow()
        XCTAssertNil(r11)
        XCTAssertEqual(ServerSettings.load().url, "https://other.example")
    }

    func testStatusAndHeartbeatDecode() throws {
        let status = try JSONDecoder().decode(ServerStatus.self, from: Data(#"{"version":"1.0.0","wire_version":2,"records":7,"server_id":"abc","preferred_url":"https://h.example"}"#.utf8))
        XCTAssertEqual(status.handoff, Handoff(serverID: "abc", preferredURL: "https://h.example"))
        let old = try JSONDecoder().decode(ServerStatus.self, from: Data(#"{"version":"0.7.0","wire_version":2,"records":7}"#.utf8))
        XCTAssertEqual(old.handoff, Handoff(serverID: nil, preferredURL: nil))
    }

    func testSameOrigin() {
        let a = URL(string: "https://health.example.ts.net/v1/samples")!
        XCTAssertTrue(BackgroundUploads.sameOrigin(a, URL(string: "https://HEALTH.example.ts.net:443/other")!))
        XCTAssertFalse(BackgroundUploads.sameOrigin(a, URL(string: "http://health.example.ts.net/v1/samples")!))
        XCTAssertFalse(BackgroundUploads.sameOrigin(a, URL(string: "https://elsewhere.example/v1/samples")!))
        XCTAssertFalse(BackgroundUploads.sameOrigin(a, URL(string: "https://health.example.ts.net:8443/v1/samples")!))
    }

    func testSetupCodeOrAdminToken() {
        XCTAssertTrue(AppModel.isSetupCode("ABCD-EFGH-JKLM-NPQR"))
        XCTAssertTrue(AppModel.isSetupCode("abcdefghjklmnpqrstuvwxyz"))
        XCTAssertTrue(AppModel.isSetupCode(" MFRG GZDF MZTW Q2LK "))
        XCTAssertFalse(AppModel.isSetupCode("Zm9vYmFyYmF6cXV4LWFkbWluLXRva2VuLTAxMjM0NTY3ODk"))
        XCTAssertFalse(AppModel.isSetupCode("SHORT"))
        XCTAssertFalse(AppModel.isSetupCode("ABCD-EFGH-JKLM-NPQ1"))
    }
}
