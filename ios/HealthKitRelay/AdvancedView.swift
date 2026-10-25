import SwiftUI

/// The technical details: the server, what is read, items shared one by one, maintenance.
struct AdvancedView: View {
    @Environment(\.dismiss) private var dismiss
    @ObservedObject private var model = AppModel.shared
    @ObservedObject private var status = SyncStatus.shared
    @State private var coverage: Coverage?
    @State private var perObjectRows: [PerObjectRow] = []
    @State private var healthRecordsAvailable = false
    @State private var healthRecordsOptedIn = false
    @State private var busy: Set<String> = []
    @State private var confirmErase = false
    @State private var confirmDisconnect = false
    @State private var showManual = false
    @State private var alert: String?

    private static let healthRecordsID = "health-records"

    var body: some View {
        NavigationStack {
            Form {
                Section("Server") {
                    LabeledContent("Server address", value: model.settings.host)
                    LabeledContent("This iPhone", value: model.device.map { "Paired as \($0.name)" } ?? "Uses the admin token")
                    if let s = model.server {
                        LabeledContent("Records on server", value: s.records.formatted())
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
                    Text("Health Relay reads every data type that Health lets apps read on this iPhone. It only reads, and leaves your Health data as it is.")
                }

                Section {
                    ForEach(perObjectRows) { row in
                        itemRow(row.name, symbol: Self.symbol(row.identifier), result: row.outcome?.text, id: row.identifier) {
                            _ = await SyncEngine.shared.requestPerObject(row.identifier)
                        }
                    }
                    #if HKR_HEALTH_RECORDS
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
                    #endif
                } header: {
                    Text("Shared item by item")
                } footer: {
                    Text("Health shares these one item at a time, for example each vision prescription, so each row has its own permission. Tap a row to choose the items to share. When Health has none of that kind yet, its list is empty."
                         + (healthRecordsAvailable ? " Health Records need a health provider connected in the Health app first." : ""))
                }

                Section {
                    if model.erasing {
                        HStack {
                            ProgressView()
                            Text("Erasing server data…").foregroundStyle(.secondary).padding(.leading, 4)
                        }
                    } else {
                        Button("Erase Server Data and Start Over", role: .destructive) { confirmErase = true }
                            .disabled(status.running)
                    }
                    ShareLink(item: Log.fileURL) {
                        Text("Share Diagnostic Log")
                    }
                } header: {
                    Text("Maintenance")
                } footer: {
                    Text(maintenanceFooter)
                }

                Section {
                    LabeledContent("Version", value: Self.appVersion)
                    LabeledContent("Data types from", value: SDKHealthTypes.sdk)
                    Link("Privacy Policy", destination: Links.privacy)
                    Link("Help", destination: Links.support)
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
            .confirmationDialog("Erase server data and start over?", isPresented: $confirmErase, titleVisibility: .visible) {
                Button("Erase and Start Over", role: .destructive) {
                    Log.info("ui: erase confirmed")
                    Task {
                        do {
                            try await model.eraseServerDataAndStartOver()
                        } catch {
                            alert = "Could not erase server data: \(error.localizedDescription)"
                        }
                    }
                }
            } message: {
                Text("This permanently deletes everything this iPhone uploaded to \(model.settings.host), then sends all its Health data again from the start. You cannot undo this.")
            }
            .confirmationDialog("Disconnect from \(model.settings.host)?", isPresented: $confirmDisconnect, titleVisibility: .visible) {
                Button("Disconnect", role: .destructive) {
                    model.disconnect()
                    dismiss()
                }
            } message: {
                Text("Syncing stops. Data already on the server stays there. The setup page keeps listing this iPhone as a paired device until you revoke it there. To connect again, scan a new setup code.")
            }
            .alert(alert ?? "", isPresented: Binding(get: { alert != nil }, set: { if !$0 { alert = nil } })) {
                Button("OK", role: .cancel) {}
            }
            .sheet(isPresented: $showManual) { ManualSetupView() }
        }
    }

    private var maintenanceFooter: String {
        let log = "The diagnostic log lists sync steps and counts only, to share when you report a problem."
        if model.erasing {
            return "The server is deleting everything this iPhone uploaded. With a long history this can take several minutes. Then Health Relay sends all Health data again. " + log
        }
        if status.running {
            return "You can erase once the running sync is done. " + log
        }
        return "Erasing permanently deletes everything this iPhone uploaded to the server, then sends all Health data again from the start. " + log
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
        #if HKR_HEALTH_RECORDS
        healthRecordsAvailable = await engine.healthRecordsAvailable()
        healthRecordsOptedIn = await engine.healthRecordsOptedIn
        #endif
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
