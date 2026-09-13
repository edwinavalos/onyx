import Foundation
import Network
import SwiftTerm
import SwiftUI

/// One VM console: a raw TCP connection to the core's upgraded
/// /v1/vms/{name}/console stream, bridged to a SwiftTerm view.
final class ConsoleConnection: ObservableObject {
    @Published var connected = false
    @Published var closedMessage: String?

    private let client: OnyxClient
    private let vmName: String
    private var conn: NWConnection?
    private var headersDone = false
    private var pending = Data()
    weak var terminal: TerminalView?

    init(client: OnyxClient, vmName: String) {
        self.client = client
        self.vmName = vmName
    }

    func connect() {
        let c = NWConnection(host: NWEndpoint.Host(client.host), port: NWEndpoint.Port(rawValue: client.port)!, using: .tcp)
        conn = c
        c.stateUpdateHandler = { [weak self] state in
            guard let self else { return }
            switch state {
            case .ready:
                c.send(content: self.client.consoleUpgradeRequest(self.vmName), completion: .contentProcessed { _ in })
                self.receive()
            case .failed(let err):
                DispatchQueue.main.async { self.closedMessage = "console: \(err.localizedDescription)"; self.connected = false }
            case .cancelled:
                DispatchQueue.main.async { self.connected = false }
            default: break
            }
        }
        c.start(queue: .global(qos: .userInteractive))
    }

    private func receive() {
        conn?.receive(minimumIncompleteLength: 1, maximumLength: 64 * 1024) { [weak self] data, _, isComplete, error in
            guard let self else { return }
            if let data, !data.isEmpty { self.handle(data) }
            if isComplete || error != nil {
                DispatchQueue.main.async {
                    self.connected = false
                    self.closedMessage = self.closedMessage ?? "console closed"
                }
                return
            }
            self.receive()
        }
    }

    private func handle(_ data: Data) {
        var payload = data
        if !headersDone {
            pending.append(data)
            guard let range = pending.range(of: Data("\r\n\r\n".utf8)) else { return }
            let head = String(decoding: pending[..<range.lowerBound], as: UTF8.self)
            payload = pending[range.upperBound...]
            pending = Data()
            headersDone = true
            if !head.hasPrefix("HTTP/1.1 101") {
                DispatchQueue.main.async { self.closedMessage = head.split(separator: "\r\n").first.map(String.init) ?? "console refused" }
                conn?.cancel()
                return
            }
            DispatchQueue.main.async { self.connected = true }
        }
        guard !payload.isEmpty else { return }
        let bytes = [UInt8](payload)
        DispatchQueue.main.async { self.terminal?.feed(byteArray: bytes[...]) }
    }

    func send(_ bytes: ArraySlice<UInt8>) {
        guard connected else { return }
        conn?.send(content: Data(bytes), completion: .contentProcessed { _ in })
    }

    func resize(rows: Int, cols: Int) {
        Task { try? await client.resize(vmName, rows: UInt16(clamping: rows), cols: UInt16(clamping: cols)) }
    }

    func close() {
        conn?.cancel()
        conn = nil
    }
}

/// TerminalView that takes keyboard focus when clicked or first shown.
final class FocusingTerminalView: TerminalView {
    var claimedFocus = false
    override func mouseDown(with event: NSEvent) {
        window?.makeFirstResponder(self)
        super.mouseDown(with: event)
    }
    override func viewDidMoveToWindow() {
        super.viewDidMoveToWindow()
        if window != nil { DispatchQueue.main.async { self.window?.makeFirstResponder(self) } }
    }
}

/// SwiftUI wrapper around SwiftTerm's TerminalView wired to a ConsoleConnection.
struct TerminalPane: NSViewRepresentable {
    @ObservedObject var console: ConsoleConnection

    func makeCoordinator() -> Coordinator { Coordinator(console: console) }

    func makeNSView(context: Context) -> FocusingTerminalView {
        let tv = FocusingTerminalView(frame: .zero)
        tv.terminalDelegate = context.coordinator
        tv.font = NSFont.monospacedSystemFont(ofSize: 12, weight: .regular)
        console.terminal = tv
        return tv
    }

    func updateNSView(_ nsView: FocusingTerminalView, context: Context) {
        // Claim keyboard focus once we are in a window.
        if nsView.window != nil, nsView.window?.firstResponder !== nsView, !nsView.claimedFocus {
            nsView.claimedFocus = true
            DispatchQueue.main.async { nsView.window?.makeFirstResponder(nsView) }
        }
    }

    final class Coordinator: NSObject, TerminalViewDelegate {
        let console: ConsoleConnection
        init(console: ConsoleConnection) { self.console = console }

        func send(source: TerminalView, data: ArraySlice<UInt8>) { console.send(data) }
        func sizeChanged(source: TerminalView, newCols: Int, newRows: Int) { console.resize(rows: newRows, cols: newCols) }
        func setTerminalTitle(source: TerminalView, title: String) {}
        func hostCurrentDirectoryUpdate(source: TerminalView, directory: String?) {}
        func scrolled(source: TerminalView, position: Double) {}
        func requestOpenLink(source: TerminalView, link: String, params: [String: String]) {
            if let url = URL(string: link) { NSWorkspace.shared.open(url) }
        }
        func bell(source: TerminalView) {}
        func clipboardCopy(source: TerminalView, content: Data) {
            NSPasteboard.general.clearContents()
            NSPasteboard.general.setString(String(decoding: content, as: UTF8.self), forType: .string)
        }
        func rangeChanged(source: TerminalView, startY: Int, endY: Int) {}
    }
}

/// Read-only tail of a VM's console log (GET /v1/vms/{name}/console_log)
/// for a VM that is not running: what its last boot printed, as plain
/// text. Reloads when the state changes (a stop lands new output).
struct ConsoleLogView: View {
    @EnvironmentObject var store: Store
    let vmName: String
    let state: String
    @State private var text: String?
    @State private var message = "Loading console log…"

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack {
                Text("Console log").font(.caption).foregroundStyle(.secondary)
                Text("last boot, read-only").font(.caption).foregroundStyle(.tertiary)
                Spacer()
                Button { Task { await load() } } label: { Image(systemName: "arrow.clockwise") }
                    .buttonStyle(.borderless).help("Reload the console log").accessibilityIdentifier("vm.log.reload")
            }
            .padding(.horizontal, 12).padding(.vertical, 6)
            Divider()
            if let text {
                ScrollViewReader { proxy in
                    ScrollView {
                        Text(text)
                            .font(.system(.caption, design: .monospaced))
                            .textSelection(.enabled)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .padding(8)
                        Color.clear.frame(height: 1).id("end")
                    }
                    .onAppear { proxy.scrollTo("end", anchor: .bottom) }
                    .onChange(of: text) { _, _ in proxy.scrollTo("end", anchor: .bottom) }
                }
                .accessibilityIdentifier("vm.log")
            } else {
                Text(message).foregroundStyle(.secondary).frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        }
        .task(id: state) { await load() }
    }

    private func load() async {
        guard let c = store.client else { return }
        do {
            text = ConsoleLogText.plain(try await c.consoleLog(vmName))
        } catch {
            text = nil
            let msg = (error as? APIError)?.message ?? error.localizedDescription
            message = msg.contains("not found") ? "No console log yet: the VM has not booted." : msg
        }
    }
}
