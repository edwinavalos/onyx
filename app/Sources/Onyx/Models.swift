import Foundation

// Wire types mirroring internal/api and internal/store (snake_case JSON).

struct VolumeMount: Codable, Hashable, Identifiable {
    var volume: String
    var target: String
    var id: String { volume + ":" + target }
}

struct VMStatus: Codable, Identifiable, Hashable {
    var name: String
    var image: String
    var cpus: UInt
    var memoryMB: UInt64
    var volumes: [VolumeMount]?
    var packs: [String]?
    var cmdline: String?
    var mac: String?
    var network: String?
    var allow: [String]?
    var state: String
    var started: Date?

    var id: String { name }
    var isRunning: Bool { state == "running" }
    var isPaused: Bool { state == "paused" }
    var isSuspended: Bool { state == "suspended" }
    /// StartVM in progress: no controls apply yet.
    var isStarting: Bool { state == "starting" }
    /// Not running: can be started (a suspended VM resumes on start).
    var isStopped: Bool { state == "stopped" || isSuspended }

    enum CodingKeys: String, CodingKey {
        case name, image, cpus, volumes, packs, cmdline, mac, network, allow, state, started
        case memoryMB = "memory_mb"
    }
}

/// VM network modes (mirrors store.Network*).
enum NetworkMode: String, CaseIterable, Identifiable {
    case nat, restricted, none
    var id: String { rawValue }
    var label: String {
        switch self {
        case .nat: return "NAT (full internet)"
        case .restricted: return "Restricted (allowlist)"
        case .none: return "None"
        }
    }
}

struct VMCreate: Codable {
    var name: String
    var image: String
    var cpus: UInt
    var memoryMB: UInt64
    var volumes: [VolumeMount]
    var packs: [String]
    var network: String = NetworkMode.nat.rawValue
    var allow: [String] = []
    enum CodingKeys: String, CodingKey {
        case name, image, cpus, volumes, packs, network, allow
        case memoryMB = "memory_mb"
    }
}

struct NamesResp: Codable { var names: [String]? }

struct PackSecret: Codable, Hashable, Identifiable {
    var key: String
    var mode: String          // env | file | proxy
    var name: String?
    var path: String?
    var perm: String?
    var upstream: String?
    var auth: String?
    var id: String { key + mode + (name ?? "") + (path ?? "") + (upstream ?? "") }

    var summary: String {
        switch mode {
        case "env": return "env \(name ?? key)"
        case "file": return "file \(path ?? "")"
        case "proxy": return "proxy \(upstream ?? "") (\(auth ?? "basic"))"
        default: return mode
        }
    }
}

struct Pack: Codable, Identifiable, Hashable {
    var name: String
    var secrets: [PackSecret]?
    var id: String { name }
}

struct KeychainRef: Codable, Hashable {
    var service: String
    var account: String?
    var jsonPath: String?
    enum CodingKeys: String, CodingKey {
        case service, account
        case jsonPath = "json_path"
    }
    static let claudeCode = KeychainRef(service: "Claude Code-credentials", account: nil, jsonPath: "claudeAiOauth.accessToken")
}

struct SecretInfo: Codable, Identifiable, Hashable {
    var key: String
    var link: KeychainRef?
    var id: String { key }
}

struct Session: Codable {
    var dir: String
    var cmd: String
    var rows: UInt16?
    var cols: UInt16?
    var onExit: String?
    enum CodingKeys: String, CodingKey {
        case dir, cmd, rows, cols
        case onExit = "on_exit"
    }
}

struct APIError: LocalizedError {
    var message: String
    var errorDescription: String? { message }
}

/// What a New VM starts with so Claude Code works out of the box: the
/// `claude` pack when one is defined and the shared state volume at
/// ~/.claude (issue #2). Run Session has the same defaults.
enum NewVMDefaults {
    // Small on purpose: sandboxes run on laptops next to everything else.
    static let cpus = 1
    static let memoryMB = 512
    static let stateVolume = "claude-state"
    static let stateVolumeSizeMB: Int64 = 20480
    static let mounts = [VolumeMount(volume: stateVolume, target: "/home/dev/.claude")]

    static func packs(available: [Pack]) -> Set<String> {
        available.contains { $0.name == "claude" } ? ["claude"] : []
    }

    /// Volumes the mounts name that must be created before the VM is.
    static func missingVolumes(_ mounts: [VolumeMount], existing: [String]) -> [String] {
        var seen = Set(existing), out: [String] = []
        for m in mounts where !seen.contains(m.volume) { seen.insert(m.volume); out.append(m.volume) }
        return out
    }
}

/// Session VMs (Run Session) are one-shot: removed once the guest powers
/// off. `sessions` maps name → "has been seen starting or running"; a VM
/// is reaped only when it is `stopped` after that, because a just-created
/// one is also `stopped` until the start request reaches the core, and
/// reaping it then deletes the directory under the start.
enum SessionReaper {
    static func reap(_ list: [VMStatus], sessions: inout [String: Bool]) -> [String] {
        var out: [String] = []
        for vm in list {
            guard let ran = sessions[vm.name] else { continue }
            if vm.isStopped {
                if ran { out.append(vm.name); sessions[vm.name] = nil }
            } else {
                sessions[vm.name] = true
            }
        }
        return out
    }
}

/// Delete-many for volumes: try every name, return the ones that failed.
enum BulkDelete {
    static func run(_ names: [String], _ remove: (String) async throws -> Void) async -> [String] {
        var failed: [String] = []
        for n in names {
            do { try await remove(n) } catch { failed.append(n) }
        }
        return failed
    }

    static func message(failed: [String]) -> String? {
        failed.isEmpty ? nil : "Could not delete: " + failed.joined(separator: ", ")
    }
}
