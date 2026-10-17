import SwiftUI

struct ContentView: View {
    @StateObject private var status = SyncStatus.shared
    @State private var settings = ServerSettings.load()
    @State private var saved = ServerSettings.load()
    @State private var needsAuthorization = true
    @State private var message: String?
    @State private var confirmReset = false
    @State private var coverage: Coverage?
    @State private var healthRecordsAvailable = false
    @State private var healthRecordsOptedIn = false

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    TextField("https://health.example.com", text: $settings.url)
                        .keyboardType(.URL)
                        .textContentType(.URL)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                    SecureField("Token", text: $settings.token)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                    Button("Save") { save() }
                        .disabled(settings == saved || !settings.isComplete)
                } header: {
                    Text("Server")
                } footer: {
                    Text("Stored in the Keychain on this phone only.")
                }

                Section {
                    if !SyncEngine.isAvailable {
                        Text("Health data is not available on this device.")
                    } else if needsAuthorization {
                        Button("Grant access") { authorize() }
                    } else {
                        Label("Access requested", systemImage: "checkmark.circle")
                    }
                    if healthRecordsAvailable {
                        if healthRecordsOptedIn {
                            Label("Health Records included", systemImage: "checkmark.circle")
                        } else {
                            Button("Include Health Records…") { authorizeHealthRecords() }
                        }
                    }
                    if let coverage {
                        LabeledContent("Types", value: "\(coverage.requested) of \(coverage.available)")
                        if coverage.notYetRequested > 0 {
                            Text("\(coverage.notYetRequested) new types: tap Grant access to include them.")
                                .font(.footnote)
                        }
                    }
                } header: {
                    Text("Health data")
                } footer: {
                    if let coverage {
                        Text(coverageFooter(coverage)
                            + (healthRecordsAvailable && !healthRecordsOptedIn
                               ? " Health Records (clinical data from your providers) are optional: they need a provider connected in the Health app first."
                               : ""))
                    }
                }

                Section("Sync") {
                    Button {
                        Task {
                            // A first sync can take long: keep the phone from locking (and
                            // HealthKit from becoming unreadable) while it runs.
                            UIApplication.shared.isIdleTimerDisabled = true
                            await SyncEngine.shared.syncAll()
                            UIApplication.shared.isIdleTimerDisabled = false
                        }
                    } label: {
                        HStack {
                            Text("Sync now")
                            if status.running {
                                Spacer()
                                ProgressView()
                            }
                        }
                    }
                    .disabled(status.running || !saved.isComplete || needsAuthorization)
                    if !status.detail.isEmpty {
                        Text(status.detail).font(.footnote)
                    }
                    LabeledContent("Last sync") {
                        if let last = status.lastSuccess {
                            Text("\(last.formatted(.relative(presentation: .named))), \(status.lastUploaded) records")
                        } else {
                            Text("Never")
                        }
                    }
                    if let error = status.lastError {
                        Text(error).font(.footnote).foregroundStyle(.red)
                    }
                }

                Section {
                    Button("Re-send all history", role: .destructive) { confirmReset = true }
                } footer: {
                    Text("Uploads everything again on the next sync. The server ignores records it already has.")
                }

                if let message {
                    Section { Text(message).font(.footnote) }
                }
            }
            .navigationTitle("Health Relay")
            .task { await refresh() }
            .confirmationDialog("Re-send all history?", isPresented: $confirmReset) {
                Button("Re-send", role: .destructive) {
                    Task { await SyncEngine.shared.resetAnchors() }
                }
            }
        }
    }

    private func save() {
        do {
            try settings.save()
            saved = settings
            message = nil
        } catch {
            message = "Could not save: \(error.localizedDescription)"
        }
    }

    private func authorize() {
        Task {
            do {
                try await SyncEngine.shared.requestAuthorization()
            } catch {
                message = error.localizedDescription
            }
            await refresh()
        }
    }

    private func authorizeHealthRecords() {
        Task {
            do {
                try await SyncEngine.shared.requestHealthRecords()
            } catch {
                message = error.localizedDescription
            }
            await refresh()
        }
    }

    private func refresh() async {
        needsAuthorization = await SyncEngine.shared.needsAuthorization()
        healthRecordsAvailable = await SyncEngine.shared.healthRecordsAvailable()
        healthRecordsOptedIn = await SyncEngine.shared.healthRecordsOptedIn
        coverage = await SyncEngine.shared.coverage()
    }

    private func coverageFooter(_ c: Coverage) -> String {
        var parts = ["Every type in the \(c.sdk) this app was built with."]
        if !c.unavailable.isEmpty { parts.append("\(c.unavailable.count) need a newer iOS.") }
        if !c.excluded.isEmpty { parts.append("\(c.excluded.count) not readable by apps: \(c.excluded.keys.sorted().map(SyncEngine.shortName).joined(separator: ", ")).") }
        parts.append("Change what is shared in Settings › Health › Data Access & Devices.")
        return parts.joined(separator: " ")
    }
}
