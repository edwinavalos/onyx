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

Working CLI on macOS/Apple Silicon: a Go core boots Alpine guests through
Virtualization.framework, attaches Onyx-managed volumes, delivers
Keychain-backed secret packs over vsock, and runs Claude Code on an
interactive console. No GUI yet. See `docs/decisions.md` for the
architecture and `docs/design-questions.md` for the reasoning.

## Quick start

Requires Go 1.26+, Docker (to build the guest image), macOS 13+ on Apple
Silicon.

```sh
make tools                      # pinned lint/security tools into ./bin
make image                      # Alpine guest image with Claude Code → images/out/
make sign                       # build + ad-hoc sign with the virtualization entitlement

./bin/onyx serve                # terminal 1: the core; VMs live as long as this runs

./bin/onyx image import base images/out
./bin/onyx secret set claude-oauth-token            # prompts; value never hits argv
./bin/onyx pack create claude -secret claude-oauth-token=CLAUDE_CODE_OAUTH_TOKEN
./bin/onyx run -pack claude                          # terminal 2: fresh VM, Claude Code on the console
```

`onyx run` creates a `<name>-work` volume mounted at `/home/dev/work` and a
shared `claude-state` volume at `/home/dev/.claude`, boots a VM, delivers the
packs, and attaches your terminal to the serial console. Ctrl-] detaches and
leaves the VM running; when the harness exits the VM is stopped and removed
(volumes persist). Everything is also available piecemeal:

```sh
onyx vm create dev -volume work:/home/dev/work -pack claude
onyx vm start dev
onyx vm exec dev -- sh -c 'echo $CLAUDE_CODE_OAUTH_TOKEN | wc -c'
onyx vm console dev
onyx cp ./myproject dev:/home/dev/work        # into the VM (lands owned by dev)
onyx cp dev:/home/dev/work/myproject ./out    # back out
onyx vm stop dev
```

Secrets are written only to tmpfs inside the guest (`/run/onyx/`); every
delivery is recorded by name in `~/Library/Application Support/Onyx/audit.log`.

## Development

```sh
make ci       # fmt, vet, lint, staticcheck, tests, gosec, govulncheck
make spike    # boot images/out end to end without the core, log to spike-console.log
```

State lives under `~/Library/Application Support/Onyx` (override with
`ONYX_HOME`; the API socket with `ONYX_SOCKET` — macOS caps socket paths at
104 bytes).
