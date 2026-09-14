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
    /// When set, the core creates the volumes in `volumes` that do not
    /// exist yet at this size. They belong to the VM until its first run:
    /// removing a VM that never came up removes them too (D18).
    var createVolumesMB: Int64? = nil
    enum CodingKeys: String, CodingKey {
        case name, image, cpus, volumes, packs, network, allow
        case memoryMB = "memory_mb"
        case createVolumesMB = "create_volumes_mb"
    }
}

/// How the Volumes page describes what attaches a volume.
enum VolumeUsage {
    static func label(users: [String]) -> String {
        users.isEmpty ? "not attached to any VM" : "used by " + users.joined(separator: ", ")
    }
}

struct NamesResp: Codable { var names: [String]? }

/// One volume as GET /v1/volumes describes it (store.VolumeInfo): the
/// size it was created with and what is actually allocated on disk.
struct VolumeInfo: Codable, Hashable, Identifiable {
    var name: String
    var sizeMB: Int64
    var usedMB: Int64
    var id: String { name }
    enum CodingKeys: String, CodingKey {
        case name
        case sizeMB = "size_mb"
        case usedMB = "used_mb"
    }

    /// "1.2 GB of 20 GB": allocated of apparent.
    var sizeLabel: String { "\(Self.format(mb: usedMB)) of \(Self.format(mb: sizeMB))" }

    /// Megabytes below a gigabyte, otherwise gigabytes with one decimal
    /// when it is not a whole number.
    static func format(mb: Int64) -> String {
        if mb < 1024 { return "\(mb) MB" }
        let gb = Double(mb) / 1024
        return gb == gb.rounded() ? "\(Int64(gb)) GB" : String(format: "%.1f GB", gb)
    }
}

/// GET /v1/volumes: names for older cores, sizes from newer ones.
struct VolumesResp: Codable {
    var names: [String]?
    var volumes: [VolumeInfo]?
}

/// Makes a raw console log readable as plain text.
enum ConsoleLogText {
    // CSI (ESC [ ... final), OSC (ESC ] ... BEL or ESC \), then any other
    // two-byte escape (ESC ( B, ESC =, ...); carriage returns last.
    private static let escapes = try! NSRegularExpression(pattern:
        "\u{1B}\\[[0-?]*[ -/]*[@-~]|\u{1B}\\][^\u{07}\u{1B}]*(?:\u{07}|\u{1B}\\\\)|\u{1B}[()][@-~]|\u{1B}[0-~]")

    static func plain(_ raw: String) -> String {
        let ns = raw as NSString
        let stripped = escapes.stringByReplacingMatches(in: raw, range: NSRange(location: 0, length: ns.length), withTemplate: "")
        return stripped.replacingOccurrences(of: "\r", with: "")
    }
}

struct PackSecret: Codable, Hashable, Identifiable {
    var key: String
    var mode: String          // env | file | proxy
    var name: String?
    var path: String?
    var perm: String?
    var upstream: String?
    var auth: String?
    var id: String { key + mode + (name ?? "") + (path ?? "") + (perm ?? "") + (upstream ?? "") + (auth ?? "") }

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

/// Provider-specific session policy. This mirrors internal/agent so the app
/// selects the same command, state path and optional default pack as the CLI
/// and MCP server.
enum CodingAgent: String, CaseIterable, Identifiable {
    case claude, codex, pi
    var id: String { rawValue }
    var label: String {
        switch self {
        case .claude: return "Claude Code"
        case .codex: return "Codex"
        case .pi: return "Pi"
        }
    }
    var command: String { rawValue }
    var stateDirectory: String { "/home/dev/." + rawValue }
    var stateVolume: String { self == .codex ? "" : rawValue + "-state" }
    var defaultPack: String { rawValue }
    var image: String { rawValue }
}

/// What a New VM starts with so the selected coding agent works out of the
/// box: its pack when one is defined and its own shared state volume. Run
/// Session has the same defaults.
enum NewVMDefaults {
    // Small on purpose: sandboxes run on laptops next to everything else.
    static let cpus = 1
    static let memoryMB = 512
    static let stateVolume = CodingAgent.claude.stateVolume
    static let stateVolumeSizeMB: Int64 = 20480
    static func image(agent: CodingAgent = .claude) -> String { agent.image }
    static func mounts(agent: CodingAgent = .claude) -> [VolumeMount] {
        agent.stateVolume.isEmpty ? [] : [VolumeMount(volume: agent.stateVolume, target: agent.stateDirectory)]
    }
    static let workRoot = "/home/dev/work"

    /// Work volumes mount at /home/dev/work/<volume>, not at /home/dev/work
    /// itself, so Claude Code (which keys its memory by working directory)
    /// keeps one project's memory apart from the next (issue #2, D14).
    static func workTarget(_ volume: String) -> String {
        volume.isEmpty ? workRoot : workRoot + "/" + volume
    }

    static func workMount(_ volume: String) -> VolumeMount {
        VolumeMount(volume: volume, target: workTarget(volume))
    }

    static func packs(agent: CodingAgent = .claude, available: [Pack]) -> Set<String> {
        available.contains { $0.name == agent.defaultPack } ? [agent.defaultPack] : []
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
