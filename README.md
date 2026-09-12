# Onyx

Onyx manages local virtual machines that host coding agents. VMs are sandboxes
with the plumbing you'd otherwise do by hand: secrets injected from the macOS
Keychain, folders shared in and out, and agent memory/config shared across VMs
and the host — all without secrets ever landing in an LLM transcript.

## Goals

- **Native virtualization first.** On macOS, use Apple's Virtualization
  framework; fall back to QEMU only where it has gaps. Linux support later.
- **Bring your own disk image.** Users run whatever images they want; Onyx is
  a nice UI for bringing VMs up and down.
- **Secret packs.** Secrets live in the macOS Keychain. Packs bundle them and
  present them to a VM at boot or when a new agent session starts.
- **Shared folders.** Mount chosen host folders into VMs with bidirectional
  changes reflected.
- **Shared agent memory/config.** Sessions and memory written in a VM are
  visible from other VMs and from the host (e.g. `claude` on the Mac sees the
  same sessions for the same project path).

## Status

Early design. macOS is the first target.

## Building

```sh
go build ./cmd/onyx
```
