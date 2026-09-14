import SwiftUI

struct SecretsView: View {
    @EnvironmentObject var store: Store
    @State private var showSet = false
    @State private var confirmDelete: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            List {
                ForEach(store.secrets) { s in
                    HStack {
                        Image(systemName: s.link == nil ? "key.fill" : "link")
                        VStack(alignment: .leading) {
                            Text(s.key)
                            if let l = s.link {
                                Text("→ \(l.service)" + (l.jsonPath.map { " [\($0)]" } ?? "")).font(.caption).foregroundStyle(.secondary)
                            }
                        }
                        Spacer()
                        Button(role: .destructive) { confirmDelete = s.key } label: { Image(systemName: "trash") }.buttonStyle(.borderless)
                            .help("Delete this secret from the Keychain")
                    }
                }
                if store.secrets.isEmpty { Text("No Onyx secrets in the Keychain").foregroundStyle(.secondary) }
            }
            Divider()
            HStack {
                Button("Add secret…") { showSet = true }
                    .help("Store a new secret in the macOS Keychain under the Onyx service; VMs get it only through a pack")
                Button("Link host Claude Code login") {
                    store.perform("link claude") { try await $0.linkSecret("claude-token", ref: .claudeCode) }
                }.help("claude-token resolves to the host's Claude Code OAuth token at use time; it is never copied")
                Spacer()
                Text("Values are stored in the macOS Keychain under service “onyx”.").font(.caption).foregroundStyle(.secondary)
            }
            .padding(10)
        }
        .navigationTitle("Keychain")
        .sheet(isPresented: $showSet) { SetSecretSheet() }
        .confirmationDialog("Delete secret \(confirmDelete ?? "")?", isPresented: Binding(get: { confirmDelete != nil }, set: { if !$0 { confirmDelete = nil } })) {
            Button("Delete", role: .destructive) {
                if let k = confirmDelete { store.perform("delete secret") { try await $0.removeSecret(k) } }
            }
        }
        .task { await store.refreshSecrets() }
    }
}

struct SetSecretSheet: View {
    @EnvironmentObject var store: Store
    @Environment(\.dismiss) private var dismiss
    @State private var key = ""
    @State private var value = ""

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Add secret").font(.title2)
            Form {
                TextField("Key (e.g. gh-token)", text: $key)
                SecureField("Value", text: $value)
            }
            HStack {
                Spacer()
                Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
                Button("Save") {
                    store.perform("set secret") { try await $0.setSecret(key, value: value) }
                    dismiss()
                }.keyboardShortcut(.defaultAction).disabled(key.isEmpty || value.isEmpty)
                    .help("Write the secret to the Keychain; the value is never shown again")
            }
        }
        .padding(20)
        .frame(width: 420)
    }
}

struct PacksView: View {
    @EnvironmentObject var store: Store
    @State private var showNew = false
    @State private var editing: Pack?
    @State private var confirmDelete: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            List {
                ForEach(store.packs) { p in
                    DisclosureGroup {
                        ForEach(Array((p.secrets ?? []).enumerated()), id: \.offset) { _, s in
                            HStack {
                                Text(s.key).font(.body.monospaced())
                                Spacer()
                                Text(s.summary).font(.caption).foregroundStyle(.secondary)
                            }
                        }
                        if (p.secrets ?? []).isEmpty { Text("empty").foregroundStyle(.secondary) }
                    } label: {
                        HStack {
                            Label(p.name, systemImage: "shippingbox")
                            Spacer()
                            Text("\((p.secrets ?? []).count) secrets").font(.caption).foregroundStyle(.secondary)
                            Button { editing = p } label: { Image(systemName: "pencil") }.buttonStyle(.borderless)
                                .help("Edit the secrets and delivery rules in this pack")
                            Button(role: .destructive) { confirmDelete = p.name } label: { Image(systemName: "trash") }.buttonStyle(.borderless)
                                .help("Delete this pack; secrets stay in the Keychain")
                        }
                    }
                }
                if store.packs.isEmpty { Text("No packs").foregroundStyle(.secondary) }
            }
            Divider()
            HStack {
                Button("New pack…") { showNew = true }
                    .help("Bundle secrets with how each is delivered to a VM (env, file, or credential proxy)")
                Spacer()
                Text("proxy = secret stays on the host; env/file = delivered to guest tmpfs").font(.caption).foregroundStyle(.secondary)
            }.padding(10)
        }
        .navigationTitle("Packs")
        .sheet(isPresented: $showNew) { PackEditorSheet() }
        .sheet(item: $editing) { PackEditorSheet(pack: $0) }
        .confirmationDialog("Delete pack \(confirmDelete ?? "")?", isPresented: Binding(get: { confirmDelete != nil }, set: { if !$0 { confirmDelete = nil } })) {
            Button("Delete", role: .destructive) {
                if let n = confirmDelete { store.perform("delete pack") { try await $0.removePack(n) } }
            }
        }
        .task { await store.refreshSecrets() }
    }
}

struct PackEditorSheet: View {
    @EnvironmentObject var store: Store
    @Environment(\.dismiss) private var dismiss
    private let existing: Pack?
    @State private var name = ""
    @State private var secrets: [PackSecret] = []
    @State private var editingIndex: Int?
    @State private var key = ""
    @State private var mode = "proxy"
    @State private var envName = ""
    @State private var path = "/run/onyx/"
    @State private var upstream = "https://github.com"
    @State private var auth = ""

    init(pack: Pack? = nil) {
        existing = pack
        _name = State(initialValue: pack?.name ?? "")
        _secrets = State(initialValue: pack?.secrets ?? [])
    }

    private var isEditing: Bool { existing != nil }
    private var entryLabel: String { editingIndex == nil ? "Add a secret" : "Edit secret" }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(isEditing ? "Edit pack" : "New pack").font(.title2)
            Form {
                TextField("Pack name", text: $name).disabled(isEditing)
                SwiftUI.Section("Secrets in this pack") {
                    ForEach(Array(secrets.enumerated()), id: \.offset) { index, s in
                        HStack {
                            Text(s.key).font(.body.monospaced())
                            Spacer()
                            Text(s.summary).font(.caption)
                            Button("Edit") { beginEditing(s, at: index) }.help("Change this secret's delivery rule")
                            Button("Remove") { remove(at: index) }.help("Drop this secret from the pack")
                        }
                    }
                    if secrets.isEmpty { Text("No secrets yet").foregroundStyle(.secondary) }
                }
                SwiftUI.Section(entryLabel) {
                    Picker("Keychain key", selection: $key) {
                        Text("—").tag("")
                        ForEach(store.secrets) { Text($0.key).tag($0.key) }
                    }
                    Picker("Mode", selection: $mode) {
                        Text("proxy (never enters the VM)").tag("proxy")
                        Text("env").tag("env")
                        Text("file").tag("file")
                    }
                    switch mode {
                    case "env": TextField("Variable name (default: key)", text: $envName)
                    case "file": TextField("Guest path", text: $path)
                    default:
                        TextField("Upstream origin", text: $upstream)
                        Picker("Auth", selection: $auth) {
                            Text("basic x-access-token (git)").tag("")
                            Text("bearer").tag("bearer")
                            Text("header x-api-key").tag("header:x-api-key")
                        }
                    }
                    HStack {
                        Button(editingIndex == nil ? "Add" : "Update") { saveEntry() }
                            .disabled(key.isEmpty)
                            .help(editingIndex == nil ? "Add this secret to the pack with the delivery mode chosen above" : "Replace this secret's delivery rule")
                        if editingIndex != nil { Button("Cancel edit") { resetEntry() } }
                    }
                }
            }
            .formStyle(.grouped)
            HStack {
                Spacer()
                Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
                Button("Save") {
                    let pack = Pack(name: name, secrets: secrets)
                    store.perform(isEditing ? "update pack" : "save pack") { client in
                        if isEditing { try await client.updatePack(pack) } else { try await client.savePack(pack) }
                    }
                    dismiss()
                }.keyboardShortcut(.defaultAction).disabled(name.isEmpty)
            }
        }
        .padding(20)
        .frame(width: 560, height: 560)
    }

    private func currentEntry() -> PackSecret {
        var entry = PackSecret(key: key, mode: mode)
        switch mode {
        case "env": entry.name = envName.isEmpty ? nil : envName
        case "file": entry.path = path
        default: entry.upstream = upstream; entry.auth = auth.isEmpty ? nil : auth
        }
        return entry
    }

    private func saveEntry() {
        let entry = currentEntry()
        if let index = editingIndex { secrets[index] = entry } else { secrets.append(entry) }
        resetEntry()
    }

    private func beginEditing(_ entry: PackSecret, at index: Int) {
        editingIndex = index
        key = entry.key
        mode = entry.mode
        envName = entry.name ?? ""
        path = entry.path ?? "/run/onyx/"
        upstream = entry.upstream ?? "https://github.com"
        auth = entry.auth ?? ""
    }

    private func remove(at index: Int) {
        secrets.remove(at: index)
        if editingIndex == index { resetEntry() }
        else if let editingIndex, editingIndex > index { self.editingIndex = editingIndex - 1 }
    }

    private func resetEntry() {
        editingIndex = nil
        key = ""
        mode = "proxy"
        envName = ""
        path = "/run/onyx/"
        upstream = "https://github.com"
        auth = ""
    }
}
