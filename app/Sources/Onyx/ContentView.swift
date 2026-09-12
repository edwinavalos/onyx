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
            detail
        }
        .sheet(isPresented: $showNewVM) { NewVMSheet() }
        .sheet(isPresented: $showRun) { RunSessionSheet(onStarted: { selection = .vm($0) }) }
        .safeAreaInset(edge: .bottom, spacing: 0) { statusBar }
        .alert("Error", isPresented: Binding(get: { store.lastError != nil }, set: { if !$0 { store.lastError = nil } })) {
            Button("OK") { store.lastError = nil }
        } message: { Text(store.lastError ?? "") }
    }

    @ViewBuilder private var detail: some View {
        if core.client == nil {
            VStack(spacing: 12) {
                if let f = core.failed {
                    Image(systemName: "exclamationmark.triangle").font(.largeTitle).foregroundStyle(.orange)
                    Text(f).multilineTextAlignment(.center)
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

    private var statusBar: some View {
        HStack {
            Circle().fill(core.client == nil ? .orange : .green).frame(width: 8, height: 8)
            Text(core.failed ?? core.status).font(.caption).foregroundStyle(.secondary)
            Spacer()
            Text(CoreProcess.stateDir.path).font(.caption2).foregroundStyle(.tertiary)
        }
        .padding(.horizontal, 10).padding(.vertical, 4)
        .frame(maxWidth: .infinity)
        .background(.bar)
        .overlay(alignment: .top) { Divider() }
    }
}

func stateColor(_ s: String) -> Color {
    switch s {
    case "running": return .green
    case "paused": return .yellow
    case "hibernated", "snapshotted": return .blue
    case "starting", "stopping": return .orange
    case "error": return .red
    default: return .gray
    }
}
