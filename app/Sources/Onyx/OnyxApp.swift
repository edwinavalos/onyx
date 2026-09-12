import SwiftUI

@main
struct OnyxApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var delegate
    @StateObject private var core: CoreProcess
    @StateObject private var store: Store

    init() {
        let core = CoreProcess()
        _core = StateObject(wrappedValue: core)
        _store = StateObject(wrappedValue: Store(core: core))
    }

    var body: some Scene {
        WindowGroup("Onyx") {
            ContentView()
                .environmentObject(core)
                .environmentObject(store)
                .frame(minWidth: 900, minHeight: 560)
                .onAppear {
                    delegate.core = core
                    delegate.store = store
                    core.start()
                    store.startPolling()
                }
                .onReceive(core.$client) { c in
                    if c != nil { Task { await store.refreshAll() } }
                }
        }
        .commands {
            CommandGroup(replacing: .newItem) {}
        }
    }
}

/// Stops the core (and therefore every VM) when the app quits.
final class AppDelegate: NSObject, NSApplicationDelegate {
    var core: CoreProcess?
    var store: Store?
    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool { true }

    /// Quitting stops every VM (D13); make that explicit when some are up.
    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        let running = (store?.vms ?? []).filter { $0.isRunning || $0.isPaused }
        // An attached core is not ours to stop: its VMs stay up.
        guard !running.isEmpty, core?.attached != true else { return .terminateNow }
        let alert = NSAlert()
        alert.messageText = "Stop \(running.count) running VM\(running.count == 1 ? "" : "s") and quit?"
        alert.informativeText = "VMs live only while Onyx is open: " + running.map(\.name).joined(separator: ", ") + ". Their volumes are kept."
        alert.addButton(withTitle: "Stop and Quit")
        alert.addButton(withTitle: "Cancel")
        return alert.runModal() == .alertFirstButtonReturn ? .terminateNow : .terminateCancel
    }
    func applicationWillTerminate(_ notification: Notification) {
        core?.stop()
    }
}
