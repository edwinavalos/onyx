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
                    }
                }
                if store.secrets.isEmpty { Text("No Onyx secrets in the Keychain").foregroundStyle(.secondary) }
            }
            Divider()
            HStack {
                Button("Add secret…") { showSet = true }
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
            }
        }
        .padding(20)
        .frame(width: 420)
    }
}

struct PacksView: View {
    @EnvironmentObject var store: Store
    @State private var showNew = false
    @State private var confirmDelete: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            List {
                ForEach(store.packs) { p in
                    DisclosureGroup {
                        ForEach(p.secrets ?? []) { s in
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
                            Button(role: .destructive) { confirmDelete = p.name } label: { Image(systemName: "trash") }.buttonStyle(.borderless)
                        }
                    }
                }
                if store.packs.isEmpty { Text("No packs").foregroundStyle(.secondary) }
            }
            Divider()
            HStack {
                Button("New pack…") { showNew = true }
                Spacer()
                Text("proxy = secret stays on the host; env/file = delivered to guest tmpfs").font(.caption).foregroundStyle(.secondary)
            }.padding(10)
        }
        .navigationTitle("Packs")
        .sheet(isPresented: $showNew) { NewPackSheet() }
        .confirmationDialog("Delete pack \(confirmDelete ?? "")?", isPresented: Binding(get: { confirmDelete != nil }, set: { if !$0 { confirmDelete = nil } })) {
            Button("Delete", role: .destructive) {
                if let n = confirmDelete { store.perform("delete pack") { try await $0.removePack(n) } }
            }
        }
        .task { await store.refreshSecrets() }
    }
}

struct NewPackSheet: View {
    @EnvironmentObject var store: Store
    @Environment(\.dismiss) private var dismiss
    @State private var name = ""
    @State private var secrets: [PackSecret] = []
    // Row being edited
    @State private var key = ""
    @State private var mode = "proxy"
    @State private var envName = ""
    @State private var path = "/run/onyx/"
    @State private var upstream = "https://github.com"
    @State private var auth = ""

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("New pack").font(.title2)
            Form {
                TextField("Pack name", text: $name)
                SwiftUI.Section("Secrets in this pack") {
                    ForEach(secrets) { s in
                        HStack { Text(s.key).font(.body.monospaced()); Spacer(); Text(s.summary).font(.caption); Button("Remove") { secrets.removeAll { $0 == s } } }
                    }
                }
                SwiftUI.Section("Add a secret") {
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
                    Button("Add") {
                        var s = PackSecret(key: key, mode: mode)
                        switch mode {
                        case "env": s.name = envName.isEmpty ? nil : envName
                        case "file": s.path = path
                        default: s.upstream = upstream; s.auth = auth.isEmpty ? nil : auth
                        }
                        secrets.append(s)
                        key = ""
                    }.disabled(key.isEmpty)
                }
            }
            .formStyle(.grouped)
            HStack {
                Spacer()
                Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
                Button("Save") {
                    store.perform("save pack") { try await $0.savePack(Pack(name: name, secrets: secrets)) }
                    dismiss()
                }.keyboardShortcut(.defaultAction).disabled(name.isEmpty)
            }
        }
        .padding(20)
        .frame(width: 560, height: 560)
    }
}
