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
    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool { true }
    func applicationWillTerminate(_ notification: Notification) {
        core?.stop()
    }
}
