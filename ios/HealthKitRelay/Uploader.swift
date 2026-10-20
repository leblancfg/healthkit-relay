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

/// Posts batches with retry and exponential backoff.
struct Uploader: Sendable {
    var settings: ServerSettings
    var maxAttempts = 5
    var session: URLSession = .shared

    func upload(_ batch: WireBatch) async throws {
        guard settings.isComplete, let url = settings.endpoint else { throw UploadError.notConfigured }
        var request = URLRequest(url: url, timeoutInterval: 120)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.setValue("Bearer \(settings.token)", forHTTPHeaderField: "Authorization")
        request.httpBody = try batch.body()

        var lastError = UploadError.failed("no attempt")
        for attempt in 0..<maxAttempts {
            if attempt > 0 {
                // 1, 2, 4, 8 s, plus up to 50% jitter.
                let delay = pow(2, Double(attempt - 1)) * Double.random(in: 1...1.5)
                try await Task.sleep(for: .seconds(delay))
            }
            do {
                let (data, response) = try await session.data(for: request)
                let code = (response as? HTTPURLResponse)?.statusCode ?? 0
                if (200..<300).contains(code) {
                    // A v1 server ignores "records" silently: never advance past data it dropped.
                    if batch.recordCount > 0 {
                        let body = try? JSONSerialization.jsonObject(with: data) as? [String: Any]
                        guard body?["records"] != nil else { throw UploadError.serverTooOld }
                    }
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
            }
        }
        throw lastError
    }

    /// Sends a heartbeat (POST /v1/heartbeat), once, briefly: it must not use up the
    /// background time a sync needs. A server before 0.6.2 answers 404, which is not an error.
    func heartbeat(_ beat: AppHeartbeat) async throws {
        guard settings.isComplete, let base = settings.baseURL else { throw UploadError.notConfigured }
        var request = URLRequest(url: base.appendingPathComponent("v1/heartbeat"), timeoutInterval: 15)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.setValue("Bearer \(settings.token)", forHTTPHeaderField: "Authorization")
        request.httpBody = try AppHeartbeat.encoder.encode(beat)
        let (data, response) = try await session.data(for: request)
        let code = (response as? HTTPURLResponse)?.statusCode ?? 0
        if (200..<300).contains(code) || code == 404 { return }
        if code == 401 { throw UploadError.unauthorized }
        throw UploadError.failed("HTTP \(code): \(String(decoding: data.prefix(300), as: UTF8.self))")
    }
}
