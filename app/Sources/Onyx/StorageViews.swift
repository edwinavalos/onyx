import SwiftUI

struct VolumesView: View {
    @EnvironmentObject var store: Store
    @State private var newName = ""
    @State private var newSize = 20480
    @State private var confirmDelete: String?

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

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            List {
                ForEach(store.volumes, id: \.self) { v in
                    HStack {
                        Image(systemName: "externaldrive")
                        Text(v)
                        Spacer()
                        if let users = attached[v] { Text("used by " + users.joined(separator: ", ")).font(.caption).foregroundStyle(.secondary) }
                        Button(role: .destructive) { confirmDelete = v } label: { Image(systemName: "trash") }
                            .buttonStyle(.borderless)
                    }
                }
                if store.volumes.isEmpty { Text("No volumes").foregroundStyle(.secondary) }
            }
            Divider()
            HStack {
                TextField("New volume name", text: $newName)
                    .accessibilityIdentifier("volume.name")
                    .onSubmit(create)
                Stepper("\(newSize) MB", value: $newSize, in: 256...1_048_576, step: 1024)
                Button("Create", action: create).disabled(newName.isEmpty).accessibilityIdentifier("volume.create")
            }
            .padding(10)
        }
        .navigationTitle("Volumes")
        .confirmationDialog("Delete volume \(confirmDelete ?? "")? Its data is lost.", isPresented: Binding(get: { confirmDelete != nil }, set: { if !$0 { confirmDelete = nil } })) {
            Button("Delete", role: .destructive) {
                if let v = confirmDelete { store.perform("delete volume") { try await $0.removeVolume(v) } }
            }
        }
        .task { await store.refreshStorage() }
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
                Button("Import folder…") { pickFolder() }.disabled(importName.isEmpty)
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
