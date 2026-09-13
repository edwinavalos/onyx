import Foundation
import SwiftUI

/// Observable snapshot of everything the core knows, refreshed by polling.
@MainActor
final class Store: ObservableObject {
    @Published var vms: [VMStatus] = []
    @Published var volumes: [String] = []
    @Published var images: [String] = []
    @Published var packs: [Pack] = []
    @Published var secrets: [SecretInfo] = []
    @Published var lastError: String?
    /// VMs created by "Run Session": removed automatically once they stop.
    /// Run Session VMs, name → seen running yet (see SessionReaper).
    @Published var sessionVMs: [String: Bool] = [:]

    let core: CoreProcess
    private var pollTask: Task<Void, Never>?

    init(core: CoreProcess) { self.core = core }

    var client: OnyxClient? { core.client }

    func startPolling() {
        pollTask?.cancel()
        pollTask = Task { [weak self] in
            while !Task.isCancelled {
                await self?.refreshVMs()
                // Poll faster while something is starting so the console
                // attaches and state changes show promptly.
                let starting = self?.vms.contains { $0.isStarting } ?? false
                try? await Task.sleep(nanoseconds: starting ? 400_000_000 : 2_000_000_000)
            }
        }
    }

    func refreshAll() async {
        await refreshVMs()
        await refreshStorage()
        await refreshSecrets()
    }

    /// Kick off a start without waiting for it: the request runs in the
    /// background and the list is refreshed as soon as the core has marked
    /// the VM "starting", so the detail view can attach its console during
    /// the boot. `session` is what the console runs once up (nil: a shell).
    func startVM(_ name: String, session: Session? = nil) {
        perform("start", refresh: false) { c in
            if let session { _ = try await c.startVM(name, session: session) } else { _ = try await c.vmAction(name, "start") }
            await self.refreshVMs()
        }
        Task {
            try? await Task.sleep(nanoseconds: 250_000_000)
            await refreshVMs()
        }
    }

    func refreshVMs() async {
        guard let c = client else { return }
        do {
            var list = try await c.listVMs().sorted { $0.name < $1.name }
            for name in SessionReaper.reap(list, sessions: &sessionVMs) {
                try? await c.removeVM(name)
                list.removeAll { $0.name == name }
            }
            vms = list
        } catch { report(error) }
    }

    func refreshStorage() async {
        guard let c = client else { return }
        do {
            volumes = try await c.listVolumes()
            images = try await c.listImages()
        } catch { report(error) }
    }

    func refreshSecrets() async {
        guard let c = client else { return }
        do {
            let names = try await c.listPacks()
            var ps: [Pack] = []
            for n in names { ps.append(try await c.getPack(n)) }
            packs = ps
            var infos: [SecretInfo] = []
            for k in try await c.listSecrets() {
                infos.append((try? await c.describeSecret(k)) ?? SecretInfo(key: k, link: nil))
            }
            secrets = infos
        } catch { report(error) }
    }

    /// Runs an API call, reports failures, and refreshes afterwards.
    func perform(_ label: String, refresh: Bool = true, _ op: @escaping (OnyxClient) async throws -> Void) {
        guard let c = client else { lastError = "core not connected"; return }
        Task {
            do { try await op(c) } catch { report(error, label) }
            if refresh { await refreshAll() }
        }
    }

    func report(_ error: Error, _ label: String? = nil) {
        let msg = (error as? APIError)?.message ?? error.localizedDescription
        lastError = label.map { "\($0): \(msg)" } ?? msg
    }
}
