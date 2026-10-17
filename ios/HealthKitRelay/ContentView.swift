import SwiftUI

struct ContentView: View {
    @StateObject private var status = SyncStatus.shared
    @State private var settings = ServerSettings.load()
    @State private var saved = ServerSettings.load()
    @State private var needsAuthorization = true
    @State private var message: String?
    @State private var confirmReset = false

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

                Section("Health data") {
                    if !SyncEngine.isAvailable {
                        Text("Health data is not available on this device.")
                    } else if needsAuthorization {
                        Button("Grant access") { authorize() }
                    } else {
                        Label("Access requested", systemImage: "checkmark.circle")
                        Text("Change which data is shared in Settings › Health › Data Access & Devices.")
                            .font(.footnote)
                            .foregroundStyle(.secondary)
                    }
                }

                Section("Sync") {
                    Button {
                        Task { await SyncEngine.shared.syncAll() }
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
            .task { needsAuthorization = await SyncEngine.shared.needsAuthorization() }
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
            needsAuthorization = await SyncEngine.shared.needsAuthorization()
        }
    }
}
