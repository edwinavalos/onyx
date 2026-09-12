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
    @Published var sessionVMs: Set<String> = []

    let core: CoreProcess
    private var pollTask: Task<Void, Never>?

    init(core: CoreProcess) { self.core = core }

    var client: OnyxClient? { core.client }

    func startPolling() {
        pollTask?.cancel()
        pollTask = Task { [weak self] in
            while !Task.isCancelled {
                await self?.refreshVMs()
                try? await Task.sleep(nanoseconds: 2_000_000_000)
            }
        }
    }

    func refreshAll() async {
        await refreshVMs()
        await refreshStorage()
        await refreshSecrets()
    }

    func refreshVMs() async {
        guard let c = client else { return }
        do {
            var list = try await c.listVMs().sorted { $0.name < $1.name }
            // Session VMs are one-shot: reap them once the guest has powered off.
            for vm in list where sessionVMs.contains(vm.name) && vm.isStopped {
                try? await c.removeVM(vm.name)
                sessionVMs.remove(vm.name)
                list.removeAll { $0.name == vm.name }
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
