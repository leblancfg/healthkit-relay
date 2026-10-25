import BackgroundTasks
import HealthKit
import XCTest
@testable import HealthKitRelay

/// Answers uploads from a script, and records what was sent.
private final class ScriptedTransport: UploadTransport, @unchecked Sendable {
    struct Sent {
        let upload: PendingUpload
        let direct: Bool
        let body: Data
    }

    private let lock = NSLock()
    private var answers: [Result<(Int, Data), Error>]
    private var log: [Sent] = []

    init(_ answers: [Result<(Int, Data), Error>]) { self.answers = answers }

    var sent: [Sent] { lock.withLock { log } }

    func send(_ request: URLRequest, body: Data, upload: PendingUpload, direct: Bool) async throws -> (Int, Data) {
        let next = lock.withLock { () -> Result<(Int, Data), Error> in
            log.append(Sent(upload: upload, direct: direct, body: body))
            return answers.isEmpty ? .success((200, Data(#"{"records":0}"#.utf8))) : answers.removeFirst()
        }
        return try next.get()
    }
}

private final class Calls<T>: @unchecked Sendable {
    private let lock = NSLock()
    private var items: [T] = []

    func add(_ item: T) { lock.withLock { items.append(item) } }
    var all: [T] { lock.withLock { items } }
}

final class BackgroundUploadsTests: XCTestCase {
    private let settings = ServerSettings(url: "http://relay.local:8080", token: "t")
    private var stores: [URL] = []

    override func tearDown() {
        stores.forEach { try? FileManager.default.removeItem(at: $0) }
    }

    private func anchorStore() -> AnchorStore {
        let s = AnchorStore(name: "anchors-test-\(UUID().uuidString)")
        stores.append(s.directory)
        return s
    }

    private func uploadStore() -> UploadStore {
        let s = UploadStore(name: "uploads-test-\(UUID().uuidString)")
        stores.append(s.directory)
        return s
    }

    private func item(_ section: WireSection, _ name: String, pad: Int = 0) throws -> WireItem {
        try WireItem(section, .object(["key": .string(name), "pad": .string(String(repeating: "x", count: pad))]))
    }

    private func batch(records: Int, samples: Int = 1) throws -> WireBatch {
        let items = try (0..<records).map { try item(.record, "r\($0)") } + (0..<samples).map { try item(.sample, "s\($0)") }
        return WireBatch(device: .object([:]), items: items, trigger: "observer", appState: "background")
    }

    private let records = Data(#"{"stored":1,"records":1}"#.utf8)

    // MARK: Batches

    func testSplitKeepsOrderAndLimits() throws {
        // Two records then their sample, three times; 150 bytes or 4 items per batch at most.
        var items: [WireItem] = []
        for i in 0..<3 {
            items += [try item(.record, "r\(i)a", pad: 20), try item(.record, "r\(i)b", pad: 20), try item(.sample, "s\(i)", pad: 20)]
        }
        let batches = WireBatch.split(items, maxBytes: 150, maxItems: 4)
        XCTAssertGreaterThan(batches.count, 1)
        XCTAssertEqual(batches.flatMap { $0 }.map(\.data), items.map(\.data), "every item once, in order: records before their sample")
        for b in batches {
            XCTAssertLessThanOrEqual(b.count, 4)
            XCTAssertLessThanOrEqual(b.reduce(0) { $0 + $1.data.count + 1 }, 150 + 1)
        }
        let big = try item(.sample, "big", pad: 500)
        XCTAssertEqual(WireBatch.split([big], maxBytes: 150, maxItems: 4).map(\.count), [1], "an item over the limit goes alone")
        XCTAssertTrue(WireBatch.split([], maxBytes: 150, maxItems: 4).isEmpty)
    }

    // MARK: Positions

    func testCommitMovesOnlyFromWhereItStarted() throws {
        let store = anchorStore()
        let first = store.commit(value: "2026-10-01", for: "summaries")
        XCTAssertNil(first.from)
        XCTAssertTrue(try store.apply(first))
        XCTAssertEqual(store.value(for: "summaries"), "2026-10-01")

        // Two uploads read from the same position: the one confirmed first moves it, the other
        // finds it moved and keeps it (no step back).
        let a = store.commit(value: "2026-10-10", for: "summaries")
        let b = store.commit(value: "2026-10-05", for: "summaries")
        XCTAssertTrue(try store.apply(a))
        XCTAssertFalse(try store.apply(b))
        XCTAssertEqual(store.value(for: "summaries"), "2026-10-10")
        XCTAssertFalse(try store.apply(a), "applied once")
    }

    func testAnchorCommitRoundTrips() throws {
        let store = anchorStore()
        let anchor = HKQueryAnchor(fromValue: 42)
        let commit = try store.commit(anchor, for: "HKQuantityTypeIdentifierHeartRate", from: store.raw("HKQuantityTypeIdentifierHeartRate", "anchor"))
        XCTAssertNil(commit.from)
        // Kept with an upload as JSON, applied by a later process.
        let saved = try JSONDecoder().decode(UploadCommit.self, from: JSONEncoder().encode(commit))
        XCTAssertEqual(saved, commit)
        XCTAssertTrue(try store.apply(saved))
        XCTAssertEqual(store.anchor(for: "HKQuantityTypeIdentifierHeartRate"), anchor)
        XCTAssertFalse(store.isEmpty)
        store.reset()
        XCTAssertTrue(store.isEmpty)
    }

    // MARK: Files

    func testUploadStoreKeepsBodyAndBookkeeping() throws {
        let store = uploadStore()
        let upload = PendingUpload(records: 2, items: 3, commit: UploadCommit(key: "k", ext: "txt", from: nil, to: Data("v".utf8)))
        let id = try store.save(Data("body".utf8), upload)
        XCTAssertEqual(try Data(contentsOf: store.bodyURL(id)), Data("body".utf8))
        XCTAssertEqual(store.load(id), upload)
        XCTAssertEqual(store.ids(), [id])
        store.remove(id)
        XCTAssertNil(store.load(id))
        XCTAssertTrue(store.ids().isEmpty)
    }

    func testOldUploadsTheSystemNoLongerHasAreRemoved() throws {
        let store = uploadStore()
        let live = try store.save(Data(), PendingUpload(records: 0, items: 1))
        let gone = try store.save(Data(), PendingUpload(records: 0, items: 1))
        XCTAssertEqual(store.removeAll(except: [live], olderThan: 60), 0, "a new upload may not have its task yet")
        XCTAssertEqual(store.removeAll(except: [live], olderThan: -1), 1)
        XCTAssertEqual(store.ids(), [live])
        XCTAssertNil(store.load(gone))
    }

    // MARK: Uploads that finish after their sync

    private func uploads(_ store: UploadStore, commits: Calls<UploadCommit>) -> BackgroundUploads {
        BackgroundUploads(identifier: "test.uploads", store: store) { commit in
            commits.add(commit)
            return true
        }
    }

    func testFinishedUploadMovesItsPositionThenTheAppSyncsOn() async throws {
        let store = uploadStore()
        let commits = Calls<UploadCommit>(), relaunches = Calls<[Int]>()
        let bg = uploads(store, commits: commits)
        bg.whenRelaunched { moved, items in relaunches.add([moved, items]) }
        let commit = UploadCommit(key: "HKQuantityTypeIdentifierStepCount", ext: "anchor", from: nil, to: Data([1]))
        let id = try store.save(Data(), PendingUpload(records: 1, items: 3, commit: commit))
        // Nobody waits for it: its sync was cut short, or ran in an earlier process.
        bg.finished(id, code: 200, answer: records, error: nil)
        XCTAssertNil(store.load(id), "files removed once over")

        let done = expectation(description: "system told")
        bg.relaunched { done.fulfill() }
        bg.urlSessionDidFinishEvents(forBackgroundURLSession: URLSession.shared)
        await fulfillment(of: [done], timeout: 5)
        XCTAssertEqual(commits.all, [commit])
        XCTAssertEqual(relaunches.all, [[1, 3]], "the next sync runs once the position moved")
    }

    func testEventsBeforeTheRelaunchCallStillFinish() async throws {
        let bg = uploads(uploadStore(), commits: Calls())
        // The session may deliver everything before the app delegate hands over its handler.
        bg.urlSessionDidFinishEvents(forBackgroundURLSession: URLSession.shared)
        let done = expectation(description: "system told")
        bg.relaunched { done.fulfill() }
        await fulfillment(of: [done], timeout: 5)
    }

    func testUnconfirmedUploadMovesNothing() async throws {
        let store = uploadStore()
        let commits = Calls<UploadCommit>()
        let bg = uploads(store, commits: commits)
        let commit = UploadCommit(key: "k", ext: "anchor", from: nil, to: Data([1]))
        for (code, answer, error) in [(503, Data(), nil), (200, Data(#"{"stored":1}"#.utf8), nil),
                                      (0, Data(), URLError(.networkConnectionLost) as Error?)] {
            let id = try store.save(Data(), PendingUpload(records: 1, items: 2, commit: commit))
            bg.finished(id, code: code, answer: answer, error: error)
            XCTAssertNil(store.load(id))
        }
        let done = expectation(description: "system told")
        bg.relaunched { done.fulfill() }
        bg.urlSessionDidFinishEvents(forBackgroundURLSession: URLSession.shared)
        await fulfillment(of: [done], timeout: 5)
        XCTAssertTrue(commits.all.isEmpty, "a server error, a v1 server dropping records, a lost connection: sent again next sync")
    }

    func testConfirms() {
        XCTAssertTrue(Uploader.confirms(200, Data(), records: 0))
        XCTAssertTrue(Uploader.confirms(201, records, records: 1))
        XCTAssertFalse(Uploader.confirms(200, Data(#"{"stored":1}"#.utf8), records: 1))
        XCTAssertFalse(Uploader.confirms(500, records, records: 1))
    }

    // MARK: Uploader

    private func uploader(_ transport: ScriptedTransport) -> Uploader {
        var u = Uploader(settings: settings, transport: transport)
        u.stillConnected = { _ in true }
        u.backoff = 0.001
        return u
    }

    func testUploadRetriesThenCarriesItsCommit() async throws {
        let t = ScriptedTransport([.success((503, Data("busy".utf8))), .failure(URLError(.timedOut)), .success((200, records))])
        let commit = UploadCommit(key: "k", ext: "txt", from: nil, to: Data("v".utf8))
        let b = try batch(records: 1)
        try await uploader(t).upload(b, commit: commit)
        XCTAssertEqual(t.sent.count, 3)
        var sentCommit = commit
        sentCommit.connection = settings.connectionKey // the connection it went to
        XCTAssertEqual(t.sent.map(\.upload), Array(repeating: PendingUpload(records: 1, items: 2, commit: sentCommit), count: 3))
        XCTAssertEqual(t.sent.first?.body, try b.body())
        XCTAssertEqual(t.sent.map(\.direct), [false, false, true], "after a failed transfer, retries go out directly")
    }

    func testAnswerWithoutBodyIsReadAgainDirectly() async throws {
        // A 2xx from the background session without its body: the batch goes once more through
        // a standard session, whose answer says whether the server kept the records.
        let t = ScriptedTransport([.success((200, Data())), .success((200, records))])
        try await uploader(t).upload(try batch(records: 2))
        XCTAssertEqual(t.sent.map(\.direct), [false, true])

        let samplesOnly = ScriptedTransport([.success((200, Data()))])
        try await uploader(samplesOnly).upload(try batch(records: 0))
        XCTAssertEqual(samplesOnly.sent.count, 1, "nothing to check without records")
    }

    func testUploadErrorsThatStop() async throws {
        let old = ScriptedTransport([.success((200, Data(#"{"stored":1}"#.utf8)))])
        do {
            try await uploader(old).upload(try batch(records: 1))
            XCTFail("a v1 server drops records")
        } catch UploadError.serverTooOld {}

        let revoked = ScriptedTransport([.success((401, Data()))])
        do {
            try await uploader(revoked).upload(try batch(records: 0))
            XCTFail("401")
        } catch UploadError.unauthorized {}
        XCTAssertEqual(revoked.sent.count, 1, "no retry")

        let offline = ScriptedTransport([.failure(UploadError.failed("no network connection"))])
        do {
            try await uploader(offline).upload(try batch(records: 0))
            XCTFail("offline")
        } catch UploadError.failed {}
        XCTAssertEqual(offline.sent.count, 1, "offline fails at once")

        let cut = ScriptedTransport([.failure(CancellationError())])
        do {
            try await uploader(cut).upload(try batch(records: 0))
            XCTFail("cancelled")
        } catch is CancellationError {}
    }

    // MARK: Configuration

    func testIdentifiersAndModesAreDeclared() {
        let info = Bundle.main.infoDictionary ?? [:]
        let permitted = info["BGTaskSchedulerPermittedIdentifiers"] as? [String] ?? []
        for kind in ScheduledTask.allCases {
            XCTAssertTrue(permitted.contains(kind.identifier), kind.identifier)
        }
        if #available(iOS 26.0, *) {
            XCTAssertTrue(permitted.contains(ContinuedSync.prefix + "*"), "continued syncs use the wildcard")
        }
        let modes = info["UIBackgroundModes"] as? [String] ?? []
        XCTAssertTrue(modes.contains("fetch"))
        XCTAssertTrue(modes.contains("processing"))
    }

    func testScheduledRequests() throws {
        let refresh = ScheduledTask.refresh.request()
        XCTAssertTrue(refresh is BGAppRefreshTaskRequest)
        XCTAssertEqual(refresh.earliestBeginDate?.timeIntervalSinceNow ?? 0, 15 * 60, accuracy: 5)
        let catchUp = try XCTUnwrap(ScheduledTask.catchUp.request() as? BGProcessingTaskRequest)
        XCTAssertTrue(catchUp.requiresExternalPower)
        XCTAssertTrue(catchUp.requiresNetworkConnectivity)
        XCTAssertEqual(ScheduledTask.catchUp.trigger, .processing)
    }

    func testNewTriggers() {
        XCTAssertEqual([SyncTrigger.processing, .urlsession, .backgrounded, .continued].map(\.rawValue),
                       ["processing", "urlsession", "backgrounded", "continued"], "the server counts these names")
    }

    func testDisconnectStopsUploads() async throws {
        let t = ScriptedTransport([.success((200, records))])
        var u = uploader(t)
        u.stillConnected = { _ in false }
        do {
            try await u.upload(try batch(records: 1))
            XCTFail("uploaded after a disconnect")
        } catch UploadError.notConfigured {}
    }

    /// A position an upload of an earlier connection carries never moves the current one's.
    func testCommitsBelongToTheirConnection() async throws {
        let saved = ServerSettings.load()
        defer { if saved.url.isEmpty { ServerSettings.clear() } else { try? saved.save() } }
        let a = ServerSettings(url: "https://a.example", token: "ta"), b = ServerSettings(url: "https://b.example", token: "tb")
        XCTAssertNotEqual(a.connectionKey, b.connectionKey)
        try b.save()
        let engine = SyncEngine.shared
        let current = await engine.owner()
        let ownsCurrent = await engine.owns(current)
        XCTAssertTrue(ownsCurrent)
        let ownsNil = await engine.owns(nil)
        XCTAssertFalse(ownsNil, "from app 0.7, with no connection: sent again rather than trusted")
        let ownsA = await engine.owns(await engine.owner(a))
        XCTAssertFalse(ownsA)
        // An erase (or a new server) resets the positions: what an earlier upload carries no
        // longer applies, even on the same connection.
        await engine.resetAnchors()
        let ownsAfterReset = await engine.owns(current)
        XCTAssertFalse(ownsAfterReset)
        var commit = UploadCommit(key: "owner-test", ext: "txt", from: nil, to: Data("v".utf8))
        commit.connection = current
        let moved = await engine.commitFinishedUpload(commit)
        XCTAssertFalse(moved)
    }
}
