import SwiftUI

enum Section: Hashable {
    case vm(String)
    case volumes, images, packs, secrets
}

struct ContentView: View {
    @EnvironmentObject var core: CoreProcess
    @EnvironmentObject var store: Store
    @State private var selection: Section? = nil
    @State private var showNewVM = false
    @State private var showRun = false

    var body: some View {
        splitView
        .sheet(isPresented: $showNewVM) { NewVMSheet() }
        .sheet(isPresented: $showRun) { RunSessionSheet(onStarted: { selection = .vm($0) }) }
        .alert("Error", isPresented: Binding(get: { store.lastError != nil }, set: { if !$0 { store.lastError = nil } })) {
            Button("OK") { store.lastError = nil }
        } message: { Text(store.lastError ?? "") }
    }

    private var splitView: some View {
        NavigationSplitView {
            List(selection: $selection) {
                SwiftUI.Section("Virtual Machines") {
                    ForEach(store.vms) { vm in
                        Label {
                            HStack {
                                Text(vm.name)
                                Spacer()
                                Text(vm.state).font(.caption).foregroundStyle(.secondary)
                            }
                        } icon: {
                            Image(systemName: "circle.fill").foregroundStyle(stateColor(vm.state)).imageScale(.small)
                        }
                        .tag(Section.vm(vm.name))
                    }
                    if store.vms.isEmpty {
                        Text("No VMs").foregroundStyle(.secondary)
                    }
                }
                SwiftUI.Section("Storage") {
                    Label("Volumes", systemImage: "externaldrive").tag(Section.volumes)
                    Label("Images", systemImage: "opticaldisc").tag(Section.images)
                }
                SwiftUI.Section("Secrets") {
                    Label("Packs", systemImage: "shippingbox").tag(Section.packs)
                    Label("Keychain", systemImage: "key").tag(Section.secrets)
                }
            }
            .listStyle(.sidebar)
            .navigationSplitViewColumnWidth(min: 200, ideal: 230)
            .toolbar {
                ToolbarItemGroup {
                    Button { showRun = true } label: { Label("Run Session", systemImage: "play.circle") }
                        .help("Fresh VM running the coding harness").accessibilityIdentifier("toolbar.run")
                    Button { showNewVM = true } label: { Label("New VM", systemImage: "plus") }
                        .help("Define a VM").accessibilityIdentifier("toolbar.newvm")
                }
            }
        } detail: {
            // No bottom bar: macOS 26's floating sidebar wants full-bleed
            // content beneath it, so core status lives in the toolbar.
            detail
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .toolbar {
                    ToolbarItem(placement: .status) { statusItem }
                }
        }
    }

    @ViewBuilder private var detail: some View {
        if core.client == nil {
            VStack(spacing: 12) {
                if let f = core.failed {
                    FailureView(message: f)
                } else {
                    ProgressView()
                    Text(core.status).foregroundStyle(.secondary)
                }
            }.padding()
        } else {
            switch selection {
            case .vm(let name):
                if let vm = store.vms.first(where: { $0.name == name }) {
                    VMDetailView(vm: vm)
                } else {
                    VStack(spacing: 8) {
                        Image(systemName: "checkmark.circle").font(.system(size: 40)).foregroundStyle(.secondary)
                        Text("Session \(name) has ended").foregroundStyle(.secondary)
                    }
                }
            case .volumes: VolumesView()
            case .images: ImagesView()
            case .packs: PacksView()
            case .secrets: SecretsView()
            case nil:
                VStack(spacing: 8) {
                    Image(systemName: "cube.transparent").font(.system(size: 48)).foregroundStyle(.secondary)
                    Text("Select a VM, or start a session").foregroundStyle(.secondary)
                }
            }
        }
    }

    /// Core connection state, one line; the state dir is in the tooltip.
    private var statusItem: some View {
        HStack(spacing: 6) {
            Circle().fill(core.client == nil ? .orange : .green).frame(width: 8, height: 8)
            Text((core.failed ?? core.status).split(separator: "\n", maxSplits: 1).first.map(String.init) ?? "")
                .font(.caption).foregroundStyle(.secondary).lineLimit(1)
        }
        .help(CoreProcess.stateDir.path)
        .accessibilityIdentifier("status.core")
    }
}

func stateColor(_ s: String) -> Color {
    switch s {
    case "running": return .green
    case "paused": return .yellow
    case "suspended": return .blue
    case "starting", "stopping": return .orange
    case "error": return .red
    default: return .gray
    }
}

/// A failure the user may need to report: the text is selectable and a
/// button copies all of it.
struct FailureView: View {
    let message: String
    @State private var copied = false

    var body: some View {
        VStack(spacing: 12) {
            Image(systemName: "exclamationmark.triangle").font(.largeTitle).foregroundStyle(.orange)
            Text(message)
                .font(.system(.body, design: .monospaced))
                .textSelection(.enabled)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(8)
                .background(.quaternary.opacity(0.5), in: RoundedRectangle(cornerRadius: 6))
            Button(copied ? "Copied" : "Copy") {
                NSPasteboard.general.clearContents()
                NSPasteboard.general.setString(message, forType: .string)
                copied = true
            }
            .accessibilityIdentifier("failure.copy")
        }
        .frame(maxWidth: 640)
    }
}
