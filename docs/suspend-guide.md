# Suspending Onyx VMs

**TL;DR:** `onyx vm suspend <name>` saves the VM's memory and device state to
a file on the host and stops it; `onyx vm start <name>` restores it and every
process carries on. Sub-second both ways for a small guest. The VM's
definition must not change in between; Onyx enforces that and refuses to
hand the VM's volumes to anyone else while it is suspended.

It uses Virtualization.framework's save/restore (macOS 14+). The guest is
not involved at all — no agent cooperation, no hibernation support needed —
so it works for any guest image.

Not needed for a laptop lid close: while `onyx serve` or the app is running,
macOS freezes the VM with the process and it continues on wake. Suspend is
for surviving the *core* going away — quitting the app, ending an MCP
session that owns VMs, or a reboot.

## Limitations, spelled out

1. **Identical configuration or nothing.** Apple: restore fails if "the
   file contents are incompatible with the current configuration". That
   includes the machine identifier (randomised per configuration unless
   persisted — Onyx persists it), the MAC, CPU count, memory size, and the
   device list. Onyx fingerprints the definition + machine id at suspend
   time; if anything differs at start, the snapshot is **discarded and the
   VM cold-boots** (processes lost, disks intact). Editing a suspended VM's
   definition is therefore the same as stopping it.
2. **Disks must not move on.** The snapshot holds RAM, including filesystem
   caches; the disks are whatever they were at the pause. If a volume is
   written by someone else before resume, the resumed guest's view of that
   filesystem is wrong and it will corrupt it. Onyx refuses to attach a
   suspended VM's volumes to another VM and refuses to delete them.
   There is no guard against editing the image files by hand.
3. **State is single-use.** The file is deleted on a successful restore.
   There is no "resume twice" or clone-from-snapshot (the disks would need
   cloning too).
4. **Time stands still inside.** The guest wakes believing no time has
   passed. Onyx's agent resets the wall clock from the host right after
   restore; monotonic clocks and timers still see no gap, so a `sleep 60`
   started before suspend finishes 60 s of *guest* time later.
5. **Network connections don't survive.** The guest keeps its IP and DHCP
   lease, but any TCP connection to the outside is dead (host-side NAT state
   is gone); programs reconnect on their own. Credential proxies are
   re-created by Onyx and the guest's loopback bridges keep working.
6. **The console scrollback is per run.** Output continues on resume; what
   was on screen before is in `console.log` only.
7. **The state file is a memory image, unencrypted.** Mode `0600` under the
   VM's directory. `env`/`file`-mode secrets are in it; proxy-mode secrets
   are never in the guest and so never in the file. Its size is the pages
   the guest has touched (tens of MB for an idle Alpine guest, up to the
   VM's full memory for a busy one).
8. **Same Mac, same macOS.** Apple documents no portability of state files
   across machines or OS versions; treat them as local and disposable.
9. **`validateSaveRestoreSupport` can say no.** "Not all configuration
   options can be safely saved and restored" — some graphics/audio device
   configurations are excluded. Onyx's device set (virtio console, blk,
   net, entropy, vsock; balloon only with `ONYX_BALLOON=1`) validates; `vm suspend` reports an error
   if a future configuration does not.
10. **Only from running.** Apple: save requires a *paused* VM, restore
    requires a *stopped* one. Onyx pauses, saves, stops for you; a VM that
    is already stopped has nothing to save.

## What Virtualization.framework does

From Apple's docs (macOS 14+):

- `saveMachineStateTo(url:)` — "Use this method to save a *paused* VM to a
  file." Fails if the VM isn't paused; on success "the VM state remains
  unchanged".
- `restoreMachineStateFrom(url:)` — "Use this method to restore a *stopped*
  VM." Fails if "the file contents are incompatible with the current
  configuration"; on success "the framework restores the VM and places it
  in the paused state" — Onyx then resumes it.
- `validateSaveRestoreSupport()` — "Not all configuration options can be
  safely saved and restored".
- `VZGenericMachineIdentifier` (macOS 13+) — "Use the data representation
  to save the VM's identifier. To restore a previously saved identifier use
  `init(dataRepresentation:)`." This is the undocumented gotcha: a rebuilt
  configuration gets a fresh random identifier and restore fails with
  `VZErrorDomain code 12 "invalid argument"`. Onyx stores it per VM in
  `vms/<name>/machine-id.bin`. `onyx probe-restore` demonstrates both
  outcomes (`ONYX_NO_MACHINE_ID=1` for the failure).

## Commands

```sh
onyx vm suspend dev      # → state: suspended; file vms/dev/state.vzs
onyx vm start dev        # restores and resumes
onyx vm pause dev / onyx vm resume dev     # freeze in RAM without stopping
```

App: **Suspend** on a running VM, **Resume** on a suspended one. MCP:
`suspend_vm`, `start_vm`.

## References

- Apple, [`saveMachineStateTo(url:completionHandler:)`](https://developer.apple.com/documentation/virtualization/vzvirtualmachine/savemachinestateto(url:completionhandler:))
- Apple, [`restoreMachineStateFrom(url:completionHandler:)`](https://developer.apple.com/documentation/virtualization/vzvirtualmachine/restoremachinestatefrom(url:completionhandler:))
- Apple, [`validateSaveRestoreSupport()`](https://developer.apple.com/documentation/virtualization/vzvirtualmachineconfiguration/validatesaverestoresupport())
- Apple, [`VZGenericMachineIdentifier`](https://developer.apple.com/documentation/virtualization/vzgenericmachineidentifier)
