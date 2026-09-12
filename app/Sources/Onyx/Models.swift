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
    var state: String
    var started: Date?

    var id: String { name }
    var isRunning: Bool { state == "running" }
    var isPaused: Bool { state == "paused" }
    var isStopped: Bool { state == "stopped" }

    enum CodingKeys: String, CodingKey {
        case name, image, cpus, volumes, packs, cmdline, mac, state, started
        case memoryMB = "memory_mb"
    }
}

struct VMCreate: Codable {
    var name: String
    var image: String
    var cpus: UInt
    var memoryMB: UInt64
    var volumes: [VolumeMount]
    var packs: [String]
    enum CodingKeys: String, CodingKey {
        case name, image, cpus, volumes, packs
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
