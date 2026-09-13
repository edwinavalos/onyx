import SwiftUI

struct VMDetailView: View {
    @EnvironmentObject var store: Store
    let vm: VMStatus
    @State private var console: ConsoleConnection?
    @State private var confirmDelete = false

    var body: some View {
        // The VM's name, spec and controls live in the window toolbar: its
        // separator is then the only horizontal rule, and nothing of ours
        // runs under macOS 26's floating sidebar.
        VStack(spacing: 0) {
            if vm.isRunning || vm.isPaused || vm.isStarting, let console {
                // Inset panel, and the black must not extend into the safe
                // area: on macOS 26 the floating sidebar is glass over the
                // detail column, so a full-bleed background tints it gray.
                TerminalPane(console: console)
                    .padding(8)
                    .background(Color.black, in: RoundedRectangle(cornerRadius: 8))
                    .overlay(alignment: .topTrailing) {
                        if let msg = console.closedMessage {
                            Text(msg).font(.caption).padding(6).background(.ultraThinMaterial).padding(8)
                        }
                    }
                    .padding(10)
            } else {
                summary
            }
        }
        .navigationTitle(vm.name)
        .navigationSubtitle("\(vm.image) · \(vm.cpus) vCPU · \(vm.memoryMB) MB · \(vm.state)")
        .toolbar { ToolbarItemGroup(placement: .primaryAction) { actions } }
        .task(id: vm.state) { attachIfRunning() }
        .onDisappear { console?.close(); console = nil }
        .confirmationDialog("Delete VM \(vm.name)? Volumes are kept.", isPresented: $confirmDelete) {
            Button("Delete", role: .destructive) { store.perform("delete") { try await $0.removeVM(vm.name) } }
                .help("Remove the VM and its root disk; volumes are kept")
        }
    }

    private func attachIfRunning() {
        // Attach as soon as the start begins: the boot is worth watching and
        // the prompt appears the moment the host finishes.
        if vm.isRunning || vm.isPaused || vm.isStarting {
            if console == nil, let c = store.client {
                let cc = ConsoleConnection(client: c, vmName: vm.name)
                cc.connect()
                console = cc
            }
        } else {
            console?.close()
            console = nil
        }
    }

    @ViewBuilder private var actions: some View {
        if vm.isStarting {
            ProgressView().controlSize(.small)
            Text("Starting…").foregroundStyle(.secondary)
            Button("Cancel") { store.perform("cancel start") { _ = try await $0.vmAction(vm.name, "stop") } }
                .help("Abort the start; the VM goes back to stopped").accessibilityIdentifier("vm.cancel")
        }
        if vm.isStopped {
            Button(vm.isSuspended ? "Resume" : "Start") { store.startVM(vm.name) }
                .help(vm.isSuspended ? "Restore the saved memory and device state and continue where it left off (⌘R)" : "Boot the VM and attach its console (⌘R)")
                .keyboardShortcut("r", modifiers: .command).accessibilityIdentifier("vm.start")
            Button("Delete", role: .destructive) { confirmDelete = true }
                .help("Remove the VM and its root disk; volumes are kept").accessibilityIdentifier("vm.delete")
        }
        if vm.isRunning {
            Button("Pause") { store.perform("pause") { _ = try await $0.vmAction(vm.name, "pause") } }
                .help("Freeze the VM in memory; Resume continues instantly").accessibilityIdentifier("vm.pause")
            Button("Suspend") { store.perform("suspend") { _ = try await $0.vmAction(vm.name, "suspend") } }
                .help("Save memory and device state to disk and stop; Resume continues every process").accessibilityIdentifier("vm.suspend")
            Button("Stop") { store.perform("stop") { _ = try await $0.vmAction(vm.name, "stop") } }
                .help("Power the VM off; running processes are lost, disks are kept").accessibilityIdentifier("vm.stop")
        }
        if vm.isPaused {
            Button("Resume") { store.perform("resume") { _ = try await $0.vmAction(vm.name, "resume") } }
                .help("Continue the paused VM")
            Button("Stop") { store.perform("stop") { _ = try await $0.vmAction(vm.name, "stop") } }
                .help("Power the VM off; running processes are lost, disks are kept")
        }
    }

    private var summary: some View {
        Form {
            LabeledContent("Image", value: vm.image)
            LabeledContent("CPUs", value: "\(vm.cpus)")
            LabeledContent("Memory", value: "\(vm.memoryMB) MB")
            LabeledContent("MAC", value: vm.mac ?? "—")
            LabeledContent("Network", value: vm.network ?? "nat")
            if vm.network == NetworkMode.restricted.rawValue {
                LabeledContent("Allowed hosts", value: (vm.allow ?? []).isEmpty ? "none (only credential proxies)" : (vm.allow ?? []).joined(separator: ", "))
            }
            LabeledContent("Volumes") {
                VStack(alignment: .trailing) {
                    ForEach(vm.volumes ?? []) { m in Text("\(m.volume) → \(m.target)") }
                    if (vm.volumes ?? []).isEmpty { Text("none") }
                }
            }
            LabeledContent("Packs", value: (vm.packs ?? []).isEmpty ? "none" : (vm.packs ?? []).joined(separator: ", "))
        }
        .formStyle(.grouped)
    }
}

/// Create a VM definition (mirrors `onyx vm create`).
struct NewVMSheet: View {
    @EnvironmentObject var store: Store
    @Environment(\.dismiss) private var dismiss
    @State private var name = Names.random()
    @State private var image = "base"
    @State private var cpus = NewVMDefaults.cpus
    @State private var memoryMB = NewVMDefaults.memoryMB
    @State private var mounts: [VolumeMount] = NewVMDefaults.mounts
    @State private var packs: Set<String> = []
    @State private var newVolume = ""
    @State private var newTarget = ""
    @State private var network: NetworkMode = .nat
    @State private var allow = ""

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("New VM").font(.title2)
            Form {
                HStack {
                    TextField("Name", text: $name).accessibilityIdentifier("newvm.name")
                    Button { name = Names.random(avoiding: Set(store.vms.map(\.name))) } label: { Image(systemName: "dice") }
                        .buttonStyle(.borderless).help("Pick another random name")
                }
                Picker("Image", selection: $image) {
                    ForEach(store.images, id: \.self) { Text($0).tag($0) }
                }
                Stepper("CPUs: \(cpus)", value: $cpus, in: 1...16)
                Stepper("Memory: \(memoryMB) MB", value: $memoryMB, in: 512...65536, step: 512)
                NetworkSection(network: $network, allow: $allow)
                SwiftUI.Section("Volumes") {
                    ForEach(mounts) { m in
                        HStack { Text("\(m.volume) → \(m.target)"); Spacer(); Button("Remove") { mounts.removeAll { $0 == m } }.help("Detach this volume from the VM definition; the volume itself is kept") }
                    }
                    HStack {
                        Picker("Volume", selection: $newVolume) {
                            Text("—").tag("")
                            ForEach(store.volumes, id: \.self) { Text($0).tag($0) }
                        }
                        // Empty path: the volume's own directory under /home/dev/work.
                        TextField(NewVMDefaults.workTarget(newVolume.isEmpty ? "<volume>" : newVolume), text: $newTarget)
                        Button("Add") {
                            let target = newTarget.isEmpty ? NewVMDefaults.workTarget(newVolume) : newTarget
                            guard !newVolume.isEmpty, target.hasPrefix("/") else { return }
                            mounts.append(VolumeMount(volume: newVolume, target: target))
                            newVolume = ""
                            newTarget = ""
                        }
                        .help("Attach the selected volume at this guest path")
                    }
                }
                SwiftUI.Section("Packs") {
                    ForEach(store.packs) { p in
                        Toggle(p.name, isOn: Binding(get: { packs.contains(p.name) }, set: { on in if on { packs.insert(p.name) } else { packs.remove(p.name) } }))
                    }
                    if store.packs.isEmpty { Text("No packs defined").foregroundStyle(.secondary) }
                }
            }
            .formStyle(.grouped)
            HStack {
                Spacer()
                Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
                Button("Create") {
                    let c = VMCreate(name: name, image: image, cpus: UInt(cpus), memoryMB: UInt64(memoryMB), volumes: mounts, packs: Array(packs).sorted(),
                                     network: network.rawValue, allow: NetworkSection.parse(allow))
                    let missing = NewVMDefaults.missingVolumes(mounts, existing: store.volumes)
                    store.perform("create vm") { client in
                        for v in missing { try await client.createVolume(v, sizeMB: NewVMDefaults.stateVolumeSizeMB) }
                        _ = try await client.createVM(c)
                    }
                    dismiss()
                }
                .keyboardShortcut(.defaultAction)
                .disabled(name.isEmpty || image.isEmpty)
                .help("Save the definition and clone the image as its root disk; missing volumes are created")
                .accessibilityIdentifier("newvm.create")
            }
        }
        .padding(20)
        .frame(width: 560, height: 560)
        .onAppear {
            if let first = store.images.first, !store.images.contains(image) { image = first }
            name = Names.random(avoiding: Set(store.vms.map(\.name)))
            packs = NewVMDefaults.packs(available: store.packs)
        }
    }
}

/// Fresh VM for one interactive session (mirrors `onyx run`).
struct RunSessionSheet: View {
    @EnvironmentObject var store: Store
    @Environment(\.dismiss) private var dismiss
    var onStarted: (String) -> Void
    @State private var name = "session-" + Self.stamp()
    @State private var image = "base"
    @State private var cmd = "claude"
    @State private var stateVolume = NewVMDefaults.stateVolume
    @State private var packs: Set<String> = []
    @State private var cpus = NewVMDefaults.cpus
    @State private var memoryMB = NewVMDefaults.memoryMB
    @State private var network: NetworkMode = .nat
    @State private var allow = ""
    @State private var busy = false

    static func stamp() -> String {
        let f = DateFormatter(); f.dateFormat = "yyyyMMdd-HHmmss"; return f.string(from: Date())
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Run Session").font(.title2)
            Text("Boots a fresh VM with a work volume, delivers the selected packs, and runs the command on the console. When it exits the VM is removed; volumes persist.")
                .font(.callout).foregroundStyle(.secondary)
            Form {
                TextField("Name", text: $name)
                Picker("Image", selection: $image) { ForEach(store.images, id: \.self) { Text($0).tag($0) } }
                TextField("Command", text: $cmd).accessibilityIdentifier("run.cmd")
                TextField("State volume (~/.claude)", text: $stateVolume)
                Stepper("CPUs: \(cpus)", value: $cpus, in: 1...16)
                Stepper("Memory: \(memoryMB) MB", value: $memoryMB, in: 512...65536, step: 512)
                NetworkSection(network: $network, allow: $allow)
                SwiftUI.Section("Packs") {
                    ForEach(store.packs) { p in
                        Toggle(p.name, isOn: Binding(get: { packs.contains(p.name) }, set: { on in if on { packs.insert(p.name) } else { packs.remove(p.name) } }))
                    }
                }
            }
            .formStyle(.grouped)
            HStack {
                Spacer()
                Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
                Button(busy ? "Starting…" : "Start") { start() }
                    .help("Create the work volume, boot the VM, and run the command on its console")
                    .keyboardShortcut(.defaultAction)
                    .disabled(busy || name.isEmpty)
                    .accessibilityIdentifier("run.start")
            }
        }
        .padding(20)
        .frame(width: 560, height: 520)
        .onAppear {
            if let first = store.images.first, !store.images.contains(image) { image = first }
            packs = NewVMDefaults.packs(available: store.packs)
        }
    }

    private func start() {
        guard let c = store.client else { return }
        busy = true
        let work = name + "-work"
        Task {
            do {
                var mounts: [VolumeMount] = []
                try? await c.createVolume(work, sizeMB: 20480)
                mounts.append(NewVMDefaults.workMount(work))
                if !stateVolume.isEmpty {
                    try? await c.createVolume(stateVolume, sizeMB: NewVMDefaults.stateVolumeSizeMB)
                    mounts.append(VolumeMount(volume: stateVolume, target: "/home/dev/.claude"))
                }
                _ = try await c.createVM(VMCreate(name: name, image: image, cpus: UInt(cpus), memoryMB: UInt64(memoryMB), volumes: mounts, packs: Array(packs).sorted(),
                                                  network: network.rawValue, allow: NetworkSection.parse(allow)))
                store.sessionVMs[name] = false
                // Show the VM now; the start runs in the background and the
                // detail view attaches its console while it boots.
                store.startVM(name, session: Session(dir: NewVMDefaults.workTarget(work), cmd: cmd, rows: 40, cols: 120, onExit: "poweroff"))
                await store.refreshVMs()
                onStarted(name)
                dismiss()
            } catch {
                store.report(error, "run session")
                busy = false
            }
        }
    }
}

/// Network mode picker plus the allowlist editor for restricted VMs
/// (shared by New VM and Run Session).
struct NetworkSection: View {
    @Binding var network: NetworkMode
    @Binding var allow: String

    var body: some View {
        SwiftUI.Section("Network") {
            Picker("Mode", selection: $network) {
                ForEach(NetworkMode.allCases) { Text($0.label).tag($0) }
            }
            .accessibilityIdentifier("newvm.network")
            if network == .restricted {
                TextField("Allowed hosts", text: $allow, prompt: Text("github.com, *.githubusercontent.com, registry.npmjs.org"))
                    .accessibilityIdentifier("newvm.allow")
                Text("No NIC. HTTP(S) only, through a host-side proxy limited to these hosts (host, *.suffix or host:port; ports 80 and 443 unless given). Credential proxies keep working. Decisions are logged to egress.log.")
                    .font(.caption).foregroundStyle(.secondary)
            } else if network == .none {
                Text("No NIC and no egress proxy. Only credential proxies reach out.")
                    .font(.caption).foregroundStyle(.secondary)
            }
        }
    }

    /// Splits the free-text allowlist on commas and whitespace.
    static func parse(_ text: String) -> [String] {
        text.split(whereSeparator: { $0 == "," || $0.isWhitespace }).map(String.init).filter { !$0.isEmpty }
    }
}
