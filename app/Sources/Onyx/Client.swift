import Foundation

/// HTTP client for the Onyx core (internal/api) over the authenticated
/// loopback listener started by `onyx serve -http`.
final class OnyxClient {
    let baseURL: URL
    let token: String
    let host: String
    let port: UInt16
    private let session: URLSession

    init(addr: String, token: String) {
        self.token = token
        let parts = addr.split(separator: ":")
        host = String(parts.first ?? "127.0.0.1")
        port = UInt16(parts.last ?? "0") ?? 0
        baseURL = URL(string: "http://\(addr)")!
        let cfg = URLSessionConfiguration.ephemeral
        cfg.timeoutIntervalForRequest = 120
        session = URLSession(configuration: cfg)
    }

    static let decoder: JSONDecoder = {
        let d = JSONDecoder()
        d.dateDecodingStrategy = .custom { dec in
            let s = try dec.singleValueContainer().decode(String.self)
            let f = ISO8601DateFormatter()
            f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
            if let d = f.date(from: s) { return d }
            f.formatOptions = [.withInternetDateTime]
            return f.date(from: s) ?? Date.distantPast
        }
        return d
    }()

    private func request(_ method: String, _ path: String, body: Data? = nil, contentType: String = "application/json") -> URLRequest {
        var r = URLRequest(url: baseURL.appendingPathComponent(path))
        r.httpMethod = method
        r.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        if let body {
            r.httpBody = body
            r.setValue(contentType, forHTTPHeaderField: "Content-Type")
        }
        return r
    }

    @discardableResult
    private func call<T: Decodable>(_ method: String, _ path: String, json: (any Encodable)? = nil, as type: T.Type = EmptyResp.self) async throws -> T {
        var body: Data? = nil
        if let json { body = try JSONEncoder().encode(AnyEncodable(json)) }
        let (data, resp) = try await session.data(for: request(method, path, body: body))
        let code = (resp as? HTTPURLResponse)?.statusCode ?? 0
        if code >= 300 {
            if let e = try? JSONDecoder().decode(ErrResp.self, from: data) {
                throw APIError(message: e.output.map { "\(e.error)\n\($0)" } ?? e.error)
            }
            throw APIError(message: "\(method) \(path): HTTP \(code)")
        }
        if T.self == EmptyResp.self { return EmptyResp() as! T }
        return try Self.decoder.decode(T.self, from: data)
    }

    func ping() async throws { try await call("GET", "/v1/ping") }

    // VMs
    func listVMs() async throws -> [VMStatus] { try await call("GET", "/v1/vms", as: [VMStatus].self) }
    func createVM(_ c: VMCreate) async throws -> VMStatus { try await call("POST", "/v1/vms", json: c, as: VMStatus.self) }
    func vmAction(_ name: String, _ action: String) async throws -> VMStatus {
        try await call("POST", "/v1/vms/\(name)/\(action)", as: VMStatus.self)
    }
    func removeVM(_ name: String) async throws { try await call("DELETE", "/v1/vms/\(name)") }
    func setSession(_ name: String, _ s: Session) async throws { try await call("POST", "/v1/vms/\(name)/session", json: s) }
    func resize(_ name: String, rows: UInt16, cols: UInt16) async throws {
        try await call("POST", "/v1/vms/\(name)/resize", json: ["rows": rows, "cols": cols])
    }
    func exec(_ name: String, _ argv: [String]) async throws -> String {
        try await call("POST", "/v1/vms/\(name)/exec", json: ["argv": argv], as: ExecResp.self).output
    }

    // Volumes / images
    func listVolumes() async throws -> [String] { try await call("GET", "/v1/volumes", as: NamesResp.self).names ?? [] }
    func createVolume(_ name: String, sizeMB: Int64) async throws {
        try await call("POST", "/v1/volumes", json: CreateVolumeReq(name: name, size_mb: sizeMB))
    }
    func removeVolume(_ name: String) async throws { try await call("DELETE", "/v1/volumes/\(name)") }
    func listImages() async throws -> [String] { try await call("GET", "/v1/images", as: NamesResp.self).names ?? [] }
    func importImage(_ name: String, dir: String) async throws {
        try await call("POST", "/v1/images", json: ["name": name, "dir": dir])
    }

    // Secrets / packs
    func listSecrets() async throws -> [String] { try await call("GET", "/v1/secrets", as: NamesResp.self).names ?? [] }
    func describeSecret(_ key: String) async throws -> SecretInfo { try await call("GET", "/v1/secrets/\(key)", as: SecretInfo.self) }
    func setSecret(_ key: String, value: String) async throws {
        try await call("PUT", "/v1/secrets", json: ["key": key, "value": value])
    }
    func linkSecret(_ key: String, ref: KeychainRef) async throws {
        try await call("PUT", "/v1/secrets/link", json: LinkReq(key: key, ref: ref))
    }
    func removeSecret(_ key: String) async throws { try await call("DELETE", "/v1/secrets/\(key)") }
    func listPacks() async throws -> [String] { try await call("GET", "/v1/packs", as: NamesResp.self).names ?? [] }
    func getPack(_ name: String) async throws -> Pack { try await call("GET", "/v1/packs/\(name)", as: Pack.self) }
    func savePack(_ p: Pack) async throws { try await call("PUT", "/v1/packs", json: p) }
    func removePack(_ name: String) async throws { try await call("DELETE", "/v1/packs/\(name)") }

    /// Raw HTTP/1.1 upgrade request for the console stream; the caller writes
    /// this to a TCP connection and then speaks raw bytes.
    func consoleUpgradeRequest(_ name: String) -> Data {
        let req = "GET /v1/vms/\(name)/console HTTP/1.1\r\nHost: onyx\r\nAuthorization: Bearer \(token)\r\nConnection: Upgrade\r\nUpgrade: onyx-console\r\n\r\n"
        return Data(req.utf8)
    }
}

struct EmptyResp: Decodable {}
struct ErrResp: Decodable { var error: String; var output: String? }
struct ExecResp: Decodable { var output: String }
struct CreateVolumeReq: Encodable { var name: String; var size_mb: Int64 }
struct LinkReq: Encodable { var key: String; var ref: KeychainRef }

/// Type-erasing wrapper so `call` can take any Encodable.
struct AnyEncodable: Encodable {
    let value: any Encodable
    init(_ v: any Encodable) { value = v }
    func encode(to encoder: Encoder) throws { try value.encode(to: encoder) }
}
