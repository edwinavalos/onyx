# Suspending Onyx VMs — the short version

**TL;DR:** you have two ways to put a VM away and get it back exactly as it
was, processes and all. Both take under a second on a 1 GB guest.

| | `onyx vm suspend` — hibernate | `onyx vm snapshot` — Vz snapshot |
|---|---|---|
| Who does the work | the **guest** kernel (Linux swsusp) | the **host** (Virtualization.framework) |
| Where the state goes | the VM's own swap disk (`/dev/vdb`) | `vms/<name>/state.vzs` on the host |
| Needs from the guest | a kernel with hibernation + the Onyx agent (our base image has both) | nothing — works with any guest image |
| If the VM definition changed before resume | the guest kernel discards the image and boots normally | Onyx discards the snapshot and boots normally (fingerprint check) |
| Resume | `onyx vm start` (kernel resumes from swap) | `onyx vm start` (Onyx restores, then resumes) |
| State shown as | `hibernated` | `snapshotted` |
| Measured (1 GB Alpine guest) | ~0.6 s save / ~0.7 s resume | ~0.7 s save / ~0.3 s resume |
| Content of the saved state | guest RAM, on the guest's disk | guest RAM, in a host file (`0600`) |

Both are single-use: after a resume the saved state is gone and the VM is
live again. Volumes, delivered secrets, credential proxies and the console
all come back in both cases (verified end to end).

**Rule of thumb:** use `suspend` (hibernate) by default — it needs nothing
on the host and is the more forgiving of the two. Use `snapshot` when the
guest can't hibernate (an image you brought yourself), or when you want the
state file somewhere you can back up or copy.

Neither is needed for a laptop lid close: while `onyx serve` or the app is
running, macOS freezes the VM with the process and it continues on wake.
Suspend/snapshot are for surviving the *core* going away — quitting the
app, ending an MCP session that owns VMs, or a reboot.

## What exactly Virtualization.framework does (snapshot)

From Apple's docs (macOS 14+):

- `saveMachineStateTo(url:)` — "Use this method to save a *paused* VM to a
  file." It fails if the VM isn't paused; on success "the VM state remains
  unchanged" (it's still paused, you stop it yourself).
- `restoreMachineStateFrom(url:)` — "Use this method to restore a *stopped*
  VM." It fails if "the file contents are incompatible with the current
  configuration" or the VM isn't stopped. On success "the framework
  restores the VM and places it in the paused state" — you then `resume()`.
- `validateSaveRestoreSupport()` — "Not all configuration options can be
  safely saved and restored"; returns false for unsupported device sets
  (e.g. some graphics/audio devices). Ours passes.
- `VZGenericMachineIdentifier` (macOS 13+) — "Use the data representation
  to save the VM's identifier. To restore a previously saved identifier use
  `init(dataRepresentation:)`."

That last one is the piece that isn't obvious anywhere: **"incompatible
with the current configuration" includes the machine identifier**, which
the framework randomises every time you build a configuration. Rebuild the
VM object with a fresh identifier and restore fails with
`VZErrorDomain code 12 "invalid argument"` no matter how identical the
devices are. Onyx persists the identifier per VM (`vms/<name>/machine-id.bin`),
which is why `snapshot` works. It also pins the NIC MAC per VM for the same
reason.

Facts worth knowing:

- The state file is small because the framework saves only pages the guest
  has touched — ~30 MB for a freshly booted Alpine guest, ~150 MB for a
  Fedora cloud image. It is a complete memory image, not a partial one.
- The save is not encrypted. Anything in guest RAM — including secrets
  delivered in `env`/`file` mode — is in that file. Proxy-mode secrets are
  never in the guest, so they are never in the file.
- Save/restore works across process restarts: the core that restores need
  not be the one that saved.
- Onyx's fingerprint (definition + machine id) is stricter than Apple's
  check. Changing CPUs, memory, volumes, packs or cmdline invalidates the
  snapshot; a stale snapshot is deleted and the VM boots fresh, never
  restored onto a disk it doesn't match.

## What guest hibernation does (suspend)

Standard Linux suspend-to-disk: the agent runs `echo disk > /sys/power/state`,
the kernel writes a compressed memory image to swap and powers off. On the
next boot the kernel command line carries `resume=/dev/vdb`; Alpine's
initramfs hands that device to the kernel, which finds the image and
restores it. If there is no valid image (or the memory size changed), it
boots normally. Every Onyx VM gets a sparse swap disk sized memory + 256 MB
for this; the same disk doubles as ordinary swap while running.

## Commands

```sh
onyx vm suspend dev      # hibernate       → state: hibernated
onyx vm snapshot dev     # Vz snapshot     → state: snapshotted
onyx vm start dev        # resumes either kind; a plain stopped VM just boots
onyx vm pause dev / onyx vm resume dev     # freeze in RAM without stopping
```

In the app: **Suspend ▾ → Hibernate (in guest) / Snapshot (on host)**;
**Resume** on a suspended VM. Over MCP: `suspend_vm`, `snapshot_vm`,
`start_vm`.

## References

- Apple, [`saveMachineStateTo(url:completionHandler:)`](https://developer.apple.com/documentation/virtualization/vzvirtualmachine/savemachinestateto(url:completionhandler:))
- Apple, [`restoreMachineStateFrom(url:completionHandler:)`](https://developer.apple.com/documentation/virtualization/vzvirtualmachine/restoremachinestatefrom(url:completionhandler:))
- Apple, [`validateSaveRestoreSupport()`](https://developer.apple.com/documentation/virtualization/vzvirtualmachineconfiguration/validatesaverestoresupport())
- Apple, [`VZGenericMachineIdentifier`](https://developer.apple.com/documentation/virtualization/vzgenericmachineidentifier)
- Linux kernel, [Power management: swsusp](https://www.kernel.org/doc/html/latest/power/swsusp.html)
- `onyx probe-restore` in this repo reproduces the identifier failure (`ONYX_NO_MACHINE_ID=1`).
