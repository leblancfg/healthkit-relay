import SwiftUI

/// The one screen: how syncing is going. Setup comes from a link; everything technical is
/// under Advanced.
struct ContentView: View {
    @ObservedObject private var model = AppModel.shared
    @Environment(\.scenePhase) private var phase
    @State private var showAdvanced = false
    @State private var showManual = false

    var body: some View {
        NavigationStack {
            Group {
                if !SyncEngine.isAvailable {
                    ContentUnavailableView("Health data is not available", systemImage: "heart.slash",
                                           description: Text("This device has no Health app."))
                } else if model.isConfigured || model.connecting != nil {
                    HomeView()
                } else {
                    WelcomeView(showManual: $showManual)
                }
            }
            .background(Color(.systemGroupedBackground))
            .toolbar {
                if model.isConfigured {
                    ToolbarItem(placement: .topBarTrailing) {
                        Button { showAdvanced = true } label: { Image(systemName: "gearshape") }
                            .accessibilityLabel("Advanced")
                            .accessibilityIdentifier("advanced")
                    }
                }
            }
            .sheet(isPresented: $showAdvanced) { AdvancedView() }
            .sheet(isPresented: $showManual) { ManualSetupView() }
            .alert("Connect to \(model.pendingLink?.host ?? "this server")?",
                   isPresented: Binding(get: { model.pendingLink != nil }, set: { if !$0 { model.pendingLink = nil } }),
                   presenting: model.pendingLink) { link in
                Button("Connect") { Task { await model.confirm(link) } }
                Button("Cancel", role: .cancel) {}
            } message: { link in
                Text("Health Relay will send all your Health data to \(link.host). Connect only if you opened this link from your own server's setup page."
                     + (link.isUnencrypted ? " This server uses plain HTTP: anyone on the network can read what is sent." : ""))
            }
        }
        .onChange(of: phase, initial: true) { _, phase in
            if phase == .active { Task { await model.becameActive() } }
        }
    }
}

// MARK: Home

struct HomeView: View {
    @ObservedObject private var model = AppModel.shared
    @ObservedObject private var status = SyncStatus.shared

    private struct Look {
        var symbol: String
        var tint: Color
        var title: String
        var subtitle: String
    }

    private var look: Look {
        if let host = model.connecting {
            return Look(symbol: "link", tint: .accentColor, title: "Connecting", subtitle: host)
        }
        if model.revoked {
            return Look(symbol: "link.badge.plus", tint: .gray, title: "Disconnected",
                        subtitle: "\(model.settings.host) no longer accepts this iPhone. Open its setup page and scan the code to connect again.")
        }
        if model.needsHealthAccess || model.askingHealth {
            return Look(symbol: "heart.fill", tint: .accentColor, title: "Allow Health access", subtitle: Self.healthHint)
        }
        if status.running {
            return Look(symbol: "arrow.triangle.2.circlepath", tint: .accentColor, title: "Syncing",
                        subtitle: status.detail.isEmpty ? "Starting…" : status.detail)
        }
        if let error = status.lastError {
            let locked = error == SyncError.locked.errorDescription
            return Look(symbol: locked ? "lock.fill" : "exclamationmark", tint: .orange,
                        title: locked ? "Waiting for unlock" : "Sync paused", subtitle: error)
        }
        if let last = status.lastSuccess {
            return Look(symbol: "checkmark", tint: .green, title: "Up to date",
                        subtitle: "Synced \(last.formatted(.relative(presentation: .named, unitsStyle: .wide)))")
        }
        return Look(symbol: "arrow.triangle.2.circlepath", tint: .accentColor, title: "Ready",
                    subtitle: "The first sync starts in a moment.")
    }

    /// What to pick in Health's sheet, which covers the app while it is up.
    static var healthHint: String {
        if #available(iOS 27.0, *) {
            return "Health asks what to share next: tap Select All, then All Recorded Data, so your whole history is copied."
        }
        return "Health asks what to share next: turn on everything, then tap Allow."
    }

    var body: some View {
        ScrollView {
            VStack(spacing: 22) {
                // Re-evaluated every 30 s so "Synced 3 minutes ago" stays true.
                TimelineView(.periodic(from: .now, by: 30)) { _ in hero(look) }
                    .padding(.top, 28)
                    .padding(.bottom, 10)
                stats
                serverCard
                if let limit = status.historyLimit {
                    Label("Health shares data since \(limit.formatted(date: .abbreviated, time: .omitted)) only. To copy older data too, allow all data for Health Relay in the Health app.",
                          systemImage: "clock.badge.exclamationmark")
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                        .padding(16)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .background(Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 22, style: .continuous))
                }
                Text("Syncs on its own in the background as new Health data arrives.")
                    .font(.footnote)
                    .foregroundStyle(.secondary)
                    .multilineTextAlignment(.center)
                    .padding(.horizontal, 24)
            }
            .padding(.horizontal, 20)
            .padding(.bottom, 20)
        }
        .refreshable { await model.sync() }
        .safeAreaInset(edge: .bottom) { syncButton }
    }

    private func hero(_ l: Look) -> some View {
        VStack(spacing: 16) {
            ZStack {
                Group {
                    Circle().stroke(l.tint.opacity(0.15), lineWidth: 6)
                    Circle()
                        .trim(from: 0, to: status.progress ?? 0)
                        .stroke(l.tint, style: StrokeStyle(lineWidth: 6, lineCap: .round))
                        .rotationEffect(.degrees(-90))
                        .animation(.easeInOut(duration: 0.4), value: status.progress)
                }
                .frame(width: 140, height: 140)
                .opacity(status.running ? 1 : 0)
                Circle()
                    .fill(l.tint.gradient)
                    .frame(width: 112, height: 112)
                    .shadow(color: l.tint.opacity(0.35), radius: 16, y: 8)
                Image(systemName: l.symbol)
                    .font(.system(size: 46, weight: .semibold))
                    .foregroundStyle(.white)
                    .symbolEffect(.pulse, isActive: status.running || model.connecting != nil)
                    .contentTransition(.symbolEffect(.replace))
            }
            .frame(height: 150)
            Text(l.title)
                .font(.largeTitle.bold())
                .accessibilityIdentifier("status.title")
            Text(l.subtitle)
                .font(.callout)
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
                .lineLimit(4)
                .frame(minHeight: 44, alignment: .top)
                .padding(.horizontal, 12)
        }
        .animation(.snappy, value: l.title)
    }

    private var stats: some View {
        HStack(spacing: 0) {
            stat(model.server.map { $0.records.formatted(.number.notation(.compactName).precision(.fractionLength(0...1))) } ?? "–",
                 "Records on server")
            Divider().frame(height: 44)
            stat(status.lastSuccess == nil ? "–" : status.lastUploaded.formatted(), "Sent last sync")
        }
        .padding(.vertical, 18)
        .background(Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 22, style: .continuous))
    }

    private func stat(_ value: String, _ label: String) -> some View {
        VStack(spacing: 4) {
            Text(value)
                .font(.title2.weight(.semibold))
                .monospacedDigit()
                .contentTransition(.numericText())
            Text(label)
                .font(.footnote)
                .foregroundStyle(.secondary)
        }
        .frame(maxWidth: .infinity)
    }

    private var serverCard: some View {
        let (text, color): (String, Color) = switch (model.revoked, model.reachable) {
        case (true, _): ("Disconnected", .gray)
        case (_, true?): ("Connected" + (model.device.map { " as \($0.name)" } ?? ""), .green)
        case (_, false?): ("Not reachable right now", .orange)
        default: ("Checking…", .secondary)
        }
        return HStack(spacing: 14) {
            Image(systemName: "server.rack")
                .font(.title3)
                .foregroundStyle(.tint)
                .frame(width: 40, height: 40)
                .background(.tint.opacity(0.12), in: RoundedRectangle(cornerRadius: 11, style: .continuous))
            VStack(alignment: .leading, spacing: 2) {
                Text(model.settings.host)
                    .font(.body.weight(.semibold))
                    .lineLimit(1)
                    .truncationMode(.middle)
                Text(text)
                    .font(.footnote)
                    .foregroundStyle(.secondary)
            }
            Spacer(minLength: 8)
            Circle().fill(color).frame(width: 9, height: 9)
        }
        .padding(16)
        .background(Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 22, style: .continuous))
    }

    @ViewBuilder private var syncButton: some View {
        Group {
            if model.needsHealthAccess {
                Button {
                    Task { await model.allowHealthAccess() }
                } label: {
                    Text("Continue")
                        .font(.headline)
                        .frame(maxWidth: .infinity)
                        .padding(.vertical, 8)
                }
                .disabled(model.askingHealth)
                .accessibilityIdentifier("health.continue")
            } else {
                Button {
                    Task { await model.sync() }
                } label: {
                    Label(status.running ? "Syncing…" : "Sync Now", systemImage: "arrow.triangle.2.circlepath")
                        .font(.headline)
                        .frame(maxWidth: .infinity)
                        .padding(.vertical, 8)
                }
                .disabled(status.running || model.connecting != nil || model.revoked || model.askingHealth)
                .accessibilityIdentifier("sync")
            }
        }
        .buttonStyle(.borderedProminent)
        .buttonBorderShape(.roundedRectangle(radius: 18))
        .padding(.horizontal, 20)
        .padding(.top, 8)
        .padding(.bottom, 4)
        .background(Color(.systemGroupedBackground))
    }
}

// MARK: Welcome

struct WelcomeView: View {
    @Binding var showManual: Bool
    @ObservedObject private var model = AppModel.shared

    var body: some View {
        ScrollView {
            VStack(spacing: 34) {
                VStack(spacing: 14) {
                    Image("Logo")
                        .resizable()
                        .frame(width: 104, height: 104)
                        .clipShape(RoundedRectangle(cornerRadius: 24, style: .continuous))
                        .shadow(color: .black.opacity(0.12), radius: 14, y: 6)
                    Text("Health Relay")
                        .font(.largeTitle.bold())
                    Text("Keeps a copy of your Health data on your own server, automatically.")
                        .font(.title3)
                        .foregroundStyle(.secondary)
                        .multilineTextAlignment(.center)
                }
                .padding(.top, 40)

                VStack(alignment: .leading, spacing: 22) {
                    step(1, "Open your server's setup page", "On a computer: your server's address, then /setup.")
                    step(2, "Scan its code with the Camera", "Then tap Open in Health Relay.")
                    step(3, "Allow Health access", "From then on, Health Relay syncs in the background.")
                }
                .padding(22)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(Color(.secondarySystemGroupedBackground), in: RoundedRectangle(cornerRadius: 22, style: .continuous))

                if let error = model.setupError {
                    Label(error, systemImage: "exclamationmark.triangle.fill")
                        .font(.callout)
                        .foregroundStyle(.orange)
                }
            }
            .padding(.horizontal, 24)
        }
        .safeAreaInset(edge: .bottom) {
            Button("Enter Server Details Instead") { showManual = true }
                .font(.callout)
                .padding(.vertical, 12)
                .frame(maxWidth: .infinity)
                .background(Color(.systemGroupedBackground))
        }
    }

    private func step(_ n: Int, _ title: String, _ detail: String) -> some View {
        HStack(alignment: .top, spacing: 14) {
            Text("\(n)")
                .font(.callout.weight(.bold))
                .foregroundStyle(.white)
                .frame(width: 28, height: 28)
                .background(Color.accentColor.gradient, in: Circle())
            VStack(alignment: .leading, spacing: 2) {
                Text(title).font(.headline)
                Text(detail).font(.subheadline).foregroundStyle(.secondary)
            }
        }
    }
}

// MARK: Manual setup

struct ManualSetupView: View {
    @Environment(\.dismiss) private var dismiss
    @ObservedObject private var model = AppModel.shared
    @State private var url = ""
    @State private var token = ""
    @State private var working = false

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    TextField("https://health.example.com", text: $url)
                        .keyboardType(.URL)
                        .textContentType(.URL)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                    SecureField("Server token", text: $token)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                } footer: {
                    Text("Your healthkit-relay server's address and token. Health Relay swaps the token for one of its own, which the server can revoke. Scanning the setup page's code does all this for you.")
                }
                if let error = model.setupError, !working {
                    Section { Text(error).foregroundStyle(.red) }
                }
            }
            .navigationTitle("Server")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
                ToolbarItem(placement: .confirmationAction) {
                    if working {
                        ProgressView()
                    } else {
                        Button("Connect") {
                            working = true
                            Task {
                                let ok = await model.connectManually(url: url, token: token)
                                working = false
                                if ok { dismiss() }
                            }
                        }
                        .disabled(url.isEmpty || token.isEmpty)
                    }
                }
            }
        }
        .onAppear { model.setupError = nil }
    }
}
