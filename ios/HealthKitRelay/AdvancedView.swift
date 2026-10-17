import SwiftUI

/// The technical details: the server, what is read, items shared one by one, maintenance.
struct AdvancedView: View {
    @Environment(\.dismiss) private var dismiss
    @ObservedObject private var model = AppModel.shared
    @State private var coverage: Coverage?
    @State private var perObjectRows: [PerObjectRow] = []
    @State private var healthRecordsAvailable = false
    @State private var healthRecordsOptedIn = false
    @State private var busy: Set<String> = []
    @State private var confirmReset = false
    @State private var confirmDisconnect = false
    @State private var showManual = false
    @State private var alert: String?

    private static let healthRecordsID = "health-records"

    var body: some View {
        NavigationStack {
            Form {
                Section("Server") {
                    LabeledContent("Address", value: model.settings.host)
                    LabeledContent("This iPhone", value: model.device.map { "Paired as \($0.name)" } ?? "Server token")
                    if let s = model.server {
                        LabeledContent("Records stored", value: s.records.formatted())
                        LabeledContent("Server version", value: s.version)
                    }
                    Button("Connect to Another Server…") { showManual = true }
                    Button("Disconnect", role: .destructive) { confirmDisconnect = true }
                }

                Section {
                    if let c = coverage {
                        NavigationLink {
                            TypesView(coverage: c)
                        } label: {
                            LabeledContent("Data types read", value: "\(c.requested) of \(c.available)")
                        }
                    } else {
                        LabeledContent("Data types read") { ProgressView() }
                    }
                } header: {
                    Text("Health data")
                } footer: {
                    Text("Every type the Health app has on this iPhone, read-only.")
                }

                Section {
                    ForEach(perObjectRows) { row in
                        itemRow(row.name, symbol: Self.symbol(row.identifier), result: row.outcome?.text, id: row.identifier) {
                            await SyncEngine.shared.requestPerObject(row.identifier)
                        }
                    }
                    if healthRecordsAvailable {
                        itemRow("Health Records", symbol: "cross.case", result: healthRecordsOptedIn ? "Included" : nil,
                                id: Self.healthRecordsID) {
                            do {
                                try await SyncEngine.shared.requestHealthRecords()
                            } catch {
                                alert = "Could not ask for Health Records: \(error.localizedDescription)"
                            }
                        }
                    }
                } header: {
                    Text("Shared item by item")
                } footer: {
                    Text("Health asks about these one item at a time, so they are not part of the main permission. Tap one to pick items to share; with none in Health there is nothing to pick. Health Records need a provider connected in the Health app.")
                }

                Section {
                    Button("Re-send All History", role: .destructive) { confirmReset = true }
                    ShareLink(item: Log.fileURL) {
                        Text("Share Diagnostic Log")
                    }
                } header: {
                    Text("Maintenance")
                } footer: {
                    Text("Re-sending uploads everything again on the next sync; the server skips what it already has. The log lists sync steps and counts, never health values.")
                }

                Section {
                    LabeledContent("Version", value: Self.appVersion)
                    LabeledContent("Data types from", value: SDKHealthTypes.sdk)
                }
            }
            .navigationTitle("Advanced")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) { Button("Done") { dismiss() } }
            }
            .task { await load() }
            .onReceive(NotificationCenter.default.publisher(for: SyncEngine.perObjectChanged)) { _ in
                Task { await load() }
            }
            .confirmationDialog("Re-send all history?", isPresented: $confirmReset, titleVisibility: .visible) {
                Button("Re-send", role: .destructive) {
                    Task {
                        await SyncEngine.shared.resetAnchors()
                        await model.sync()
                    }
                }
            }
            .confirmationDialog("Disconnect from \(model.settings.host)?", isPresented: $confirmDisconnect, titleVisibility: .visible) {
                Button("Disconnect", role: .destructive) {
                    model.disconnect()
                    dismiss()
                }
            } message: {
                Text("Syncing stops. Data already on the server stays there.")
            }
            .alert(alert ?? "", isPresented: Binding(get: { alert != nil }, set: { if !$0 { alert = nil } })) {
                Button("OK", role: .cancel) {}
            }
            .sheet(isPresented: $showManual) { ManualSetupView() }
        }
    }

    private func itemRow(_ name: String, symbol: String, result: String?, id: String,
                         action: @escaping () async -> Void) -> some View {
        Button {
            guard !busy.contains(id) else { return }
            busy.insert(id)
            Log.info("ui: \(id) tapped")
            Task {
                await action()
                busy.remove(id)
                await load()
            }
        } label: {
            HStack {
                Label(name, systemImage: symbol)
                Spacer()
                if busy.contains(id) {
                    ProgressView()
                } else if let result {
                    Text(result).foregroundStyle(.secondary).lineLimit(2).multilineTextAlignment(.trailing)
                } else {
                    Text("Choose…").foregroundStyle(.tint)
                }
            }
        }
        .foregroundStyle(.primary)
        .disabled(busy.contains(id))
    }

    private func load() async {
        let engine = SyncEngine.shared
        perObjectRows = await engine.perObjectRows()
        healthRecordsAvailable = await engine.healthRecordsAvailable()
        healthRecordsOptedIn = await engine.healthRecordsOptedIn
        coverage = await engine.coverage()
    }

    private static func symbol(_ identifier: String) -> String {
        switch identifier {
        case "HKVisionPrescriptionTypeIdentifier": "eyeglasses"
        case "HKDataTypeUserAnnotatedMedicationConcept", "HKDataTypeIdentifierUserAnnotatedMedicationConcept": "pills"
        default: "doc.text"
        }
    }

    static var appVersion: String {
        let info = Bundle.main.infoDictionary
        return "\(info?["CFBundleShortVersionString"] as? String ?? "?") (\(info?["CFBundleVersion"] as? String ?? "?"))"
    }
}

/// Which types are read, and why the others are not.
struct TypesView: View {
    let coverage: Coverage

    var body: some View {
        List {
            Section("Read (\(coverage.readable.count))") {
                ForEach(names(coverage.readable), id: \.self) { Text($0) }
            }
            if !coverage.excluded.isEmpty {
                Section("Not readable by apps (\(coverage.excluded.count))") {
                    ForEach(names(Array(coverage.excluded.keys)), id: \.self) { Text($0) }
                }
            }
            if !coverage.unavailable.isEmpty {
                Section {
                    ForEach(names(coverage.unavailable), id: \.self) { Text($0) }
                } header: {
                    Text("Needs a newer iOS (\(coverage.unavailable.count))")
                }
            }
        }
        .navigationTitle("Data Types")
        .navigationBarTitleDisplayMode(.inline)
    }

    private func names(_ ids: [String]) -> [String] {
        ids.map(SyncEngine.readableName).sorted()
    }
}
