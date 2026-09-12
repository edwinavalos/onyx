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

Spike stage. The end-to-end path works on macOS/Apple Silicon: a Go binary
boots an Alpine guest through Virtualization.framework, attaches an
Onyx-managed volume, and drives the in-guest agent over vsock. See
`docs/decisions.md` for the architecture and `docs/design-questions.md` for
the reasoning.

## Building

Requires Go 1.26+, Docker (for the guest image), and macOS 13+ on Apple
Silicon.

```sh
make tools    # pinned lint/security tools into ./bin
make image    # Alpine guest image → images/out/
make spike    # build, ad-hoc sign, boot a VM, talk to the guest agent, shut down
make ci       # fmt, vet, lint, staticcheck, tests, gosec, govulncheck
```

`make spike -- ` writes the guest serial console to `spike-console.log`. For
an interactive console: `make sign && ./bin/onyx spike -interactive`.
