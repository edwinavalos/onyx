import XCTest
@testable import Onyx

/// Wire-format tests: the app must decode exactly what the core's API
/// emits (internal/core.VMStatus, snake_case), including fields added
/// later, and encode what it sends the same way.
final class ModelsTests: XCTestCase {
    func testDecodesVMStatusFromCore() throws {
        let json = """
        {"name":"rvm","image":"base","cpus":2,"memory_mb":1024,
         "cmdline":"console=hvc0","mac":"1a:e7:91:ae:d9:30",
         "network":"restricted","allow":["github.com","*.githubusercontent.com"],
         "volumes":[{"volume":"work","target":"/home/dev/work"}],"packs":["claude"],
         "state":"running","started":"2026-09-12T16:00:54.561689-07:00"}
        """
        let vm = try OnyxClient.decoder.decode(VMStatus.self, from: Data(json.utf8))
        XCTAssertEqual(vm.name, "rvm")
        XCTAssertEqual(vm.memoryMB, 1024)
        XCTAssertEqual(vm.network, "restricted")
        XCTAssertEqual(vm.allow, ["github.com", "*.githubusercontent.com"])
        XCTAssertEqual(vm.volumes?.first?.target, "/home/dev/work")
        XCTAssertTrue(vm.isRunning)
        XCTAssertNotNil(vm.started)
    }

    /// Every state the core reports maps to exactly one set of controls.
    func testStateFlags() {
        func vm(_ state: String) -> VMStatus {
            try! OnyxClient.decoder.decode(VMStatus.self, from: Data(
                #"{"name":"x","image":"base","cpus":1,"memory_mb":1,"state":"\#(state)"}"#.utf8))
        }
        XCTAssertTrue(vm("stopped").isStopped)
        XCTAssertTrue(vm("suspended").isStopped)
        XCTAssertTrue(vm("suspended").isSuspended)
        XCTAssertTrue(vm("running").isRunning)
        XCTAssertTrue(vm("paused").isPaused)
        // Regression: a VM the core is still starting must show as such and
        // offer no Start/Delete/Stop controls.
        let starting = vm("starting")
        XCTAssertTrue(starting.isStarting)
        XCTAssertFalse(starting.isStopped)
        XCTAssertFalse(starting.isRunning)
        XCTAssertFalse(starting.isPaused)
    }

    func testEncodesVMCreateSnakeCase() throws {
        let c = VMCreate(name: "n", image: "base", cpus: 2, memoryMB: 512, volumes: [], packs: ["p"],
                         network: "restricted", allow: ["github.com"])
        let obj = try JSONSerialization.jsonObject(with: JSONEncoder().encode(c)) as? [String: Any]
        XCTAssertEqual(obj?["memory_mb"] as? Int, 512)
        XCTAssertEqual(obj?["network"] as? String, "restricted")
        XCTAssertEqual(obj?["allow"] as? [String], ["github.com"])
        XCTAssertNil(obj?["memoryMB"])
    }

    func testAllowlistParsing() {
        XCTAssertEqual(NetworkSection.parse("github.com, *.npmjs.org  registry.example:8080\n"),
                       ["github.com", "*.npmjs.org", "registry.example:8080"])
        XCTAssertEqual(NetworkSection.parse(""), [])
    }
}

/// New VM must default to a working Claude Code setup (issue #2): the
/// `claude` pack when it exists and the shared state volume at ~/.claude.
final class NewVMDefaultsTests: XCTestCase {
    func testPacksDefaultToClaudeWhenDefined() {
        XCTAssertEqual(NewVMDefaults.packs(available: [Pack(name: "github"), Pack(name: "claude")]), ["claude"])
        XCTAssertEqual(NewVMDefaults.packs(available: [Pack(name: "github")]), [])
    }

    func testStateVolumeMountedAtClaudeHome() {
        XCTAssertEqual(NewVMDefaults.mounts, [VolumeMount(volume: "claude-state", target: "/home/dev/.claude")])
    }

    /// Volumes named in the mounts that do not exist yet are created before
    /// the VM is; existing ones are left alone.
    func testMissingVolumes() {
        let mounts = [VolumeMount(volume: "claude-state", target: "/home/dev/.claude"), VolumeMount(volume: "work", target: "/home/dev/work")]
        XCTAssertEqual(NewVMDefaults.missingVolumes(mounts, existing: ["work"]), ["claude-state"])
        XCTAssertEqual(NewVMDefaults.missingVolumes(mounts, existing: ["work", "claude-state"]), [])
    }
}
