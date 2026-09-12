import Foundation

/// Launches and supervises the Onyx core (`onyx-core serve -http ...`) as a
/// child of the app, per decisions.md D13: VMs live exactly as long as the
/// app. Connection details come from <state dir>/serve.json. If a core is
/// already serving (started by `onyx mcp` or `onyx serve`), the app attaches
/// to it instead; that core's VMs then outlive the app.
@MainActor
final class CoreProcess: ObservableObject {
    @Published private(set) var client: OnyxClient?
    @Published private(set) var status: String = "starting core…"
    @Published private(set) var failed: String?

    private var process: Process?
    private var logHandle: FileHandle?

    static var stateDir: URL {
        if let env = ProcessInfo.processInfo.environment["ONYX_HOME"], !env.isEmpty {
            return URL(fileURLWithPath: env)
        }
        return FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent("Library/Application Support/Onyx")
    }

    /// The core binary: bundled next to the app executable, or ONYX_CORE for development.
    static var coreBinary: URL? {
        if let env = ProcessInfo.processInfo.environment["ONYX_CORE"], !env.isEmpty {
            return URL(fileURLWithPath: env)
        }
        let bundled = Bundle.main.bundleURL.appendingPathComponent("Contents/MacOS/onyx-core")
        if FileManager.default.isExecutableFile(atPath: bundled.path) { return bundled }
        // Running from `swift build`: look for ./bin/onyx relative to the package.
        let dev = URL(fileURLWithPath: FileManager.default.currentDirectoryPath).appendingPathComponent("../bin/onyx").standardized
        if FileManager.default.isExecutableFile(atPath: dev.path) { return dev }
        return nil
    }

    /// True when the app is using a core it did not start.
    @Published private(set) var attached = false

    func start() {
        guard process == nil, client == nil else { return }
        let dir = Self.stateDir
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let infoURL = dir.appendingPathComponent("serve.json")
        Task {
            if let c = await Self.existingCore(infoURL) {
                attached = true
                client = c
                status = "attached to a running core"
                return
            }
            spawn(dir: dir, infoURL: infoURL)
        }
    }

    /// A core that is already serving, if serve.json points at a live one.
    private static func existingCore(_ infoURL: URL) async -> OnyxClient? {
        guard let data = try? Data(contentsOf: infoURL),
              let info = try? JSONDecoder().decode(ServeInfo.self, from: data) else { return nil }
        let c = OnyxClient(addr: info.addr, token: info.token)
        return (try? await c.ping()) != nil ? c : nil
    }

    private func spawn(dir: URL, infoURL: URL) {
        guard let bin = Self.coreBinary else {
            failed = "onyx core binary not found (bundle Contents/MacOS/onyx-core, or set ONYX_CORE)"
            return
        }
        try? FileManager.default.removeItem(at: infoURL)

        let p = Process()
        p.executableURL = bin
        p.arguments = ["serve", "-http", "127.0.0.1:0", "-with-parent"]
        var env = ProcessInfo.processInfo.environment
        env["ONYX_HOME"] = dir.path
        p.environment = env
        let log = dir.appendingPathComponent("serve.log")
        FileManager.default.createFile(atPath: log.path, contents: nil)
        if let h = try? FileHandle(forWritingTo: log) {
            h.seekToEndOfFile()
            p.standardOutput = h
            p.standardError = h
            logHandle = h
        }
        p.terminationHandler = { [weak self] proc in
            let tail = Self.tail(of: log, lines: 15)
            Task { @MainActor in
                self?.client = nil
                self?.failed = "core exited (status \(proc.terminationStatus)); see \(log.path)\n\n\(tail)"
            }
        }
        do {
            try p.run()
        } catch {
            failed = "could not start core: \(error.localizedDescription)"
            return
        }
        process = p
        status = "waiting for core…"
        Task { await waitForServeInfo(infoURL) }
    }

    private func waitForServeInfo(_ url: URL) async {
        for _ in 0..<80 {
            // The termination handler has already explained what went wrong.
            if failed != nil { return }
            if let data = try? Data(contentsOf: url),
               let info = try? JSONDecoder().decode(ServeInfo.self, from: data) {
                let c = OnyxClient(addr: info.addr, token: info.token)
                if (try? await c.ping()) != nil {
                    client = c
                    status = "connected to core at \(info.addr)"
                    return
                }
            }
            try? await Task.sleep(nanoseconds: 250_000_000)
        }
        if failed == nil { failed = "core did not come up in time; see \(url.deletingLastPathComponent().appendingPathComponent("serve.log").path)" }
    }

    /// Stops the core, which stops every VM. Blocks briefly so the process
    /// has a chance to shut VMs down cleanly before the app exits.
    func stop() {
        guard let p = process, p.isRunning else { return }
        p.interrupt() // SIGINT → serve shuts VMs down
        let deadline = Date().addingTimeInterval(20)
        while p.isRunning && Date() < deadline {
            Thread.sleep(forTimeInterval: 0.1)
        }
        if p.isRunning { p.terminate() }
        process = nil
        client = nil
    }

    /// The last few lines of a log file, so the failure view can show why.
    nonisolated private static func tail(of url: URL, lines n: Int) -> String {
        guard let text = try? String(contentsOf: url, encoding: .utf8) else { return "" }
        return text.split(separator: "\n", omittingEmptySubsequences: true).suffix(n).joined(separator: "\n")
    }
}

private struct ServeInfo: Decodable { var addr: String; var token: String }
