import Foundation

enum UploadError: LocalizedError {
    case notConfigured
    case rejected(Int, String)   // the server refused the batch; retrying will not help
    case failed(String)          // network or server trouble; worth retrying later

    var errorDescription: String? {
        switch self {
        case .notConfigured: "Server URL or token missing"
        case let .rejected(code, msg): "Server rejected the upload (\(code)): \(msg)"
        case let .failed(msg): "Upload failed: \(msg)"
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
        let body = try JSONEncoder().encode(batch)
        var request = URLRequest(url: url, timeoutInterval: 60)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.setValue("Bearer \(settings.token)", forHTTPHeaderField: "Authorization")
        request.httpBody = body

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
                if (200..<300).contains(code) { return }
                let msg = String(decoding: data.prefix(300), as: UTF8.self)
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
}
