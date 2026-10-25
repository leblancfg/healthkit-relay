import Foundation

enum UploadError: LocalizedError {
    case notConfigured
    case rejected(Int, String)   // the server refused the batch; retrying will not help
    case failed(String)          // network or server trouble; worth retrying later
    case serverTooOld
    case unauthorized             // token revoked or replaced

    var errorDescription: String? {
        switch self {
        case .notConfigured: "Health Relay is not connected to a server. Open your server's setup page and scan its setup code."
        case let .rejected(code, msg): "The server refused the data (error \(code)). The server's log says why. Server's answer: \(msg)"
        case let .failed(msg): "Could not send data to the server. Health Relay tries again at the next sync. Reason: \(msg)"
        case .serverTooOld: "The server runs an older healthkit-relay than this app needs. Update healthkit-relay on the server, then tap Sync Now."
        case .unauthorized: "The server no longer accepts this iPhone. To connect again, open the server's setup page and scan its setup code."
        }
    }
}

/// Posts batches with retry and exponential backoff. Batches go through the background
/// session (BackgroundUploads), heartbeats through `session`.
struct Uploader: Sendable {
    var settings: ServerSettings
    var maxAttempts = 5
    /// For heartbeats; follows no redirect, like every request with the token.
    var session: URLSession = AddressHandoff.noRedirects
    var transport: UploadTransport = BackgroundUploads.shared
    /// The first retry's delay; each next one doubles it.
    var backoff: Double = 1

    /// Whether an answer confirms a batch with `records` records: 2xx, and a server that knows
    /// records (a v1 server ignores them silently: never advance past data it dropped).
    static func confirms(_ code: Int, _ answer: Data, records: Int) -> Bool {
        guard (200..<300).contains(code) else { return false }
        guard records > 0 else { return true }
        let body = try? JSONSerialization.jsonObject(with: answer) as? [String: Any]
        return body?["records"] != nil
    }

    /// Sends `batch`. `commit` is the sync position the batch completes, applied by
    /// BackgroundUploads if the upload finishes after the sync waiting for it is gone.
    func upload(_ batch: WireBatch, commit: UploadCommit? = nil) async throws {
        guard settings.isComplete, let url = settings.endpoint else { throw UploadError.notConfigured }
        var request = URLRequest(url: url, timeoutInterval: 120)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.setValue("Bearer \(settings.token)", forHTTPHeaderField: "Authorization")
        let body = try batch.body()
        let upload = PendingUpload(records: batch.recordCount, items: batch.count, commit: commit)

        var lastError = UploadError.failed("no attempt")
        var direct = false
        for attempt in 0..<maxAttempts {
            if attempt > 0 {
                // 1, 2, 4, 8 s, plus up to 50% jitter.
                let delay = backoff * pow(2, Double(attempt - 1)) * Double.random(in: 1...1.5)
                try await Task.sleep(for: .seconds(delay))
            }
            do {
                var (code, data) = try await transport.send(request, body: body, upload: upload, direct: direct)
                if (200..<300).contains(code), data.isEmpty, batch.recordCount > 0, !direct {
                    // A background session may leave out the answer's body: to know whether the
                    // server kept the records, the batch goes again through a standard session
                    // (the server keeps it once).
                    Log.info("upload: accepted without an answer; sending once more to read it")
                    direct = true
                    (code, data) = try await transport.send(request, body: body, upload: upload, direct: true)
                }
                if (200..<300).contains(code) {
                    guard Self.confirms(code, data, records: batch.recordCount) else { throw UploadError.serverTooOld }
                    return
                }
                let msg = String(decoding: data.prefix(300), as: UTF8.self)
                if code == 401 { throw UploadError.unauthorized }
                if code == 408 || code == 429 || code >= 500 {
                    lastError = .failed("HTTP \(code): \(msg)")
                    continue
                }
                throw UploadError.rejected(code, msg)
            } catch let e as UploadError {
                throw e
            } catch is CancellationError {
                throw CancellationError()
            } catch {
                lastError = .failed(error.localizedDescription)
                // Retries go out at once, through a standard session: a background session
                // may hold a transfer back.
                direct = true
            }
        }
        throw lastError
    }

    /// Sends a heartbeat (POST /v1/heartbeat), once, briefly: it must not use up the
    /// background time a sync needs. A server before 0.6.2 answers 404, which is not an error.
    /// Returns where the server asks to be reached (server 1.0+; nil before).
    @discardableResult
    func heartbeat(_ beat: AppHeartbeat) async throws -> Handoff? {
        guard settings.isComplete, let base = settings.baseURL else { throw UploadError.notConfigured }
        var request = URLRequest(url: base.appendingPathComponent("v1/heartbeat"), timeoutInterval: 15)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.setValue("Bearer \(settings.token)", forHTTPHeaderField: "Authorization")
        request.httpBody = try AppHeartbeat.encoder.encode(beat)
        let (data, response) = try await session.data(for: request)
        let code = (response as? HTTPURLResponse)?.statusCode ?? 0
        if (200..<300).contains(code) { return try? JSONDecoder().decode(Handoff.self, from: data) }
        if code == 404 { return nil }
        if code == 401 { throw UploadError.unauthorized }
        throw UploadError.failed("HTTP \(code): \(String(decoding: data.prefix(300), as: UTF8.self))")
    }
}
