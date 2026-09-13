import SwiftUI

struct VolumesView: View {
    @EnvironmentObject var store: Store
    @State private var newName = ""
    @State private var newSize = 20480
    @State private var selection: Set<String> = []
    @State private var confirmDelete: [String] = []

    private func create() {
        let name = newName.trimmingCharacters(in: .whitespaces)
        guard !name.isEmpty else { return }
        store.perform("create volume") { try await $0.createVolume(name, sizeMB: Int64(newSize)) }
        newName = ""
    }

    private var attached: [String: [String]] {
        var m: [String: [String]] = [:]
        for vm in store.vms { for mnt in vm.volumes ?? [] { m[mnt.volume, default: []].append(vm.name) } }
        return m
    }

    /// Selected volumes in list order (the selection set is unordered).
    private var selected: [String] { store.volumes.filter { selection.contains($0) } }

    private func delete(_ names: [String]) {
        store.perform("delete volumes") { c in
            let failed = await BulkDelete.run(names) { try await c.removeVolume($0) }
            if let msg = BulkDelete.message(failed: failed) { throw APIError(message: msg) }
        }
        selection.subtract(names)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            // Multi-select: ⌘-click / shift-click, or ⌘A for all.
            List(selection: $selection) {
                ForEach(store.volumeInfos) { info in
                    let v = info.name
                    HStack {
                        Image(systemName: "externaldrive")
                        Text(v)
                        Spacer()
                        Text(VolumeUsage.label(users: attached[v] ?? [])).font(.caption).foregroundStyle(.secondary)
                        if info.sizeMB > 0 {
                            Text(info.sizeLabel).font(.caption).foregroundStyle(.secondary).monospacedDigit()
                                .help("Space allocated on disk of the volume's full size (images are sparse)")
                        }
                        Button(role: .destructive) { confirmDelete = [v] } label: { Image(systemName: "trash") }.help("Delete this volume and everything on it")
                            .buttonStyle(.borderless)
                    }
                    .tag(v)
                }
                if store.volumes.isEmpty { Text("No volumes").foregroundStyle(.secondary) }
            }
            Divider()
            HStack {
                TextField("New volume name", text: $newName)
                    .accessibilityIdentifier("volume.name")
                    .onSubmit(create)
                Stepper("\(newSize) MB", value: $newSize, in: 256...1_048_576, step: 1024)
                Button("Create", action: create).disabled(newName.isEmpty).help("Create an empty disk image VMs can attach").accessibilityIdentifier("volume.create")
            }
            .padding(10)
            Divider()
            HStack {
                Button("Select All") { selection = Set(store.volumes) }
                    .disabled(store.volumes.isEmpty).help("Select every volume (⌘A in the list does the same)")
                Spacer()
                Button("Delete Selected (\(selection.count))", role: .destructive) { confirmDelete = selected }
                    .disabled(selection.isEmpty).help("Delete the selected volumes and everything on them")
                    .accessibilityIdentifier("volume.deleteSelected")
                Button("Delete All…", role: .destructive) { confirmDelete = store.volumes }
                    .disabled(store.volumes.isEmpty).help("Delete every volume, including claude-state and all work volumes")
                    .accessibilityIdentifier("volume.deleteAll")
            }
            .padding(10)
        }
        .navigationTitle("Volumes")
        .confirmationDialog(deleteTitle, isPresented: Binding(get: { !confirmDelete.isEmpty }, set: { if !$0 { confirmDelete = [] } })) {
            Button(confirmDelete.count == 1 ? "Delete" : "Delete \(confirmDelete.count) Volumes", role: .destructive) { delete(confirmDelete) }
        } message: {
            if confirmDelete.count > 1 { Text(confirmDelete.joined(separator: ", ")) }
        }
        .task { await store.refreshStorage() }
    }

    private var deleteTitle: String {
        confirmDelete.count == 1 ? "Delete volume \(confirmDelete[0])? Its data is lost." : "Delete \(confirmDelete.count) volumes? Their data is lost."
    }
}

struct ImagesView: View {
    @EnvironmentObject var store: Store
    @State private var importName = "base"

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            List {
                ForEach(store.images, id: \.self) { i in Label(i, systemImage: "opticaldisc") }
                if store.images.isEmpty {
                    Text("No images installed. Build one with `make image`, then import images/out.").foregroundStyle(.secondary)
                }
            }
            Divider()
            HStack {
                TextField("Image name", text: $importName)
                Button("Import folder…") { pickFolder() }.disabled(importName.isEmpty).help("Install a guest image from a folder with vmlinux, initramfs, and rootfs.img (make image builds one into images/out)")
            }
            .padding(10)
        }
        .navigationTitle("Images")
        .task { await store.refreshStorage() }
    }

    private func pickFolder() {
        let p = NSOpenPanel()
        p.canChooseDirectories = true
        p.canChooseFiles = false
        p.message = "Choose a folder containing vmlinux, initramfs and rootfs.img"
        if p.runModal() == .OK, let url = p.url {
            store.perform("import image") { try await $0.importImage(importName, dir: url.path) }
        }
    }
}
