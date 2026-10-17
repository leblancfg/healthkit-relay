import SwiftUI

struct ContentView: View {
    @StateObject private var status = SyncStatus.shared
    @State private var settings = ServerSettings.load()
    @State private var saved = ServerSettings.load()
    @State private var needsAuthorization = true
    @State private var message: String?
    @State private var confirmReset = false
    @State private var coverage: Coverage?
    @State private var authorizing = false
    @State private var alert: String?
    @State private var healthRecordsAvailable = false
    @State private var healthRecordsOptedIn = false
    @State private var healthRecordsBusy = false
    @State private var accessNote: String?
    @State private var perObjectRows: [PerObjectRow] = []
    @State private var perObjectBusy: Set<String> = []

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
                    } else {
                        // Also a button once answered: Health then shows a sheet only for
                        // types it never asked about, else returns at once.
                        Button {
                            authorize()
                        } label: {
                            HStack {
                                if needsAuthorization {
                                    Text("Grant access")
                                } else {
                                    Label("Access requested", systemImage: "checkmark.circle")
                                }
                                if authorizing {
                                    Spacer()
                                    ProgressView()
                                }
                            }
                        }
                        .disabled(authorizing)
                    }
                    if let accessNote {
                        Text(accessNote).font(.footnote)
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
                        Text(coverageFooter(coverage))
                    }
                }

                if healthRecordsAvailable || !perObjectRows.isEmpty {
                    Section {
                        if healthRecordsAvailable {
                            optionalRow("Health Records", busy: healthRecordsBusy,
                                        result: healthRecordsOptedIn ? "Included" : nil) { authorizeHealthRecords() }
                        }
                        ForEach(perObjectRows) { row in
                            optionalRow(row.name, busy: perObjectBusy.contains(row.identifier),
                                        result: row.outcome?.text) { authorizePerObject(row.identifier) }
                        }
                    } header: {
                        Text("Optional")
                    } footer: {
                        Text("Asked one by one, apart from Grant access: Health lists your items and you pick which to share. "
                            + "Health Records need a provider connected in the Health app first.")
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
                    .disabled(status.running || !saved.isComplete)
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
            .onReceive(NotificationCenter.default.publisher(for: SyncEngine.perObjectChanged)) { _ in
                Task { await refresh() }
            }
            .alert(alert ?? "", isPresented: Binding(get: { alert != nil }, set: { if !$0 { alert = nil } })) {
                Button("OK", role: .cancel) {}
            }
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

    private func optionalRow(_ name: String, busy: Bool, result: String?, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            HStack {
                Text(name + "…")
                Spacer()
                if busy {
                    ProgressView()
                } else if let result {
                    Text(result).foregroundStyle(.secondary).lineLimit(2).multilineTextAlignment(.trailing)
                }
            }
        }
        .disabled(busy)
    }

    private func authorize() {
        guard !authorizing else { return }
        authorizing = true
        accessNote = nil
        Log.info("ui: Grant access tapped")
        Task {
            do {
                if try await !SyncEngine.shared.requestAuthorization() {
                    accessNote = "Health had nothing new to ask. To change what is shared: Settings › Health › Data Access & Devices › Health Relay."
                }
            } catch {
                Log.error("ui: authorization failed: \(error)")
                alert = "Could not request access: \(error.localizedDescription)"
            }
            needsAuthorization = await SyncEngine.shared.needsAuthorization()
            authorizing = false
            Log.info("ui: Grant access settled: \(needsAuthorization ? "Grant access" : "Access requested")")
            await refresh()
        }
    }

    private func authorizeHealthRecords() {
        guard !healthRecordsBusy else { return }
        healthRecordsBusy = true
        Log.info("ui: Health Records tapped")
        Task {
            do {
                try await SyncEngine.shared.requestHealthRecords()
            } catch {
                Log.error("ui: health records authorization failed: \(error)")
                alert = "Could not request Health Records: \(error.localizedDescription)"
            }
            healthRecordsBusy = false
            await refresh()
        }
    }

    private func authorizePerObject(_ identifier: String) {
        guard !perObjectBusy.contains(identifier) else { return }
        perObjectBusy.insert(identifier)
        Log.info("ui: \(identifier) tapped")
        Task {
            let outcome = await SyncEngine.shared.requestPerObject(identifier)
            Log.info("ui: \(identifier) settled: \(outcome.text)")
            perObjectBusy.remove(identifier)
            await refresh()
        }
    }

    private func refresh() async {
        needsAuthorization = await SyncEngine.shared.needsAuthorization()
        perObjectRows = await SyncEngine.shared.perObjectRows()
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
