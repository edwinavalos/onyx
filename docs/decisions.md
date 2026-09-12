# Onyx architecture decisions

Answers to `design-questions.md`, distilled. Each entry is the decision, the
reason, and what it rules out. Revisit by editing the entry, not by adding a
new one.

## D1. Virtualization: Apple Virtualization.framework via cgo (Code-Hex/vz)

Go calls Vz through `github.com/Code-Hex/vz` (cgo + Obj-C shims). We write
our own shims only where vz is missing something. QEMU is **out** of v1 —
the only gap that mattered (nested virt) is unavailable on Vz anyway.

Requires the binary to carry `com.apple.security.virtualization`. Ad-hoc
signing (`make sign`) is enough for local dev; Developer ID is deferred until
distribution matters.

## D2. Guest: Linux, kernel + initrd + raw disk

`VZLinuxBootLoader` with an explicit kernel/initrd; root disk is a raw image.
Rootfs build scripts are lifted from `rubbish` where they fit. Users can
bring their own image as long as it boots this way and runs the guest agent
(D5).

## D3. Networking: NAT only in v1

`VZNATNetworkDeviceAttachment`. Egress allowlisting is tracked in
[issue #1](https://github.com/edwinavalos/onyx/issues/1).

## D4. Threat model: protect the host from the agent

Priority order: (1) host isolation — the VM boundary; (2) secrets not
leaking into transcripts — best effort via D6; (3) network — deferred (D3).
The reason to use Onyx over `docker run` is (1) plus the plumbing.

## D5. Guest agent is required

A small Onyx agent runs in every guest. It owns: user/uid setup, mount
setup, secret fetching, session lifecycle signals. This constrains "bring
your own image" — v1 ships a base image; portability of the agent to
arbitrary images is a later problem.

## D6. Secrets: vsock fetch-on-demand now, credential proxy next

Transport is vsock between host daemon and guest agent. Secrets live in the
macOS Keychain; a **pack** is a named set of secrets plus, per secret, a
delivery `mode`:

- `env` — exported into the harness's environment (v1). Leaks if the agent
  runs `env`; accepted for now.
- `file` — written to a tmpfs path (v1).
- `proxy` — the guest never receives the value; a host-side helper
  substitutes it (git credential helper first, then HTTP proxy header
  injection, then cloud-CLI `credential_process`). This is the real answer
  to "keep it out of the transcript" and is built after `env`/`file` work.

Every fetch is logged on the host: VM id, pack, secret name, timestamp
(audit for free). Packs are attached to a VM config. Running VMs re-fetch on
session start; deleting a Keychain item means the next fetch fails.

## D7. Keychain access

Prompts happen once per code signature per item, not per access. During
development with ad-hoc signing every rebuild re-prompts. Mitigation: isolate
Keychain reads in a small, rarely rebuilt helper; consider the `security` CLI
in dev mode.

## D8. Storage: Onyx-managed volumes, not host directory mounts

No virtio-fs, no sync layer, nothing reflected back to the Mac filesystem.
Like Docker volumes: a **volume** is a named raw disk image file owned by
Onyx (`~/Library/Application Support/Onyx/volumes/<name>.img`), attached to
a VM as a virtio-blk device, formatted ext4 by the guest agent on first use.

- Volumes persist across VM restarts and can be moved between VMs.
- A volume is attached to **one running VM at a time** (block devices can't
  be shared). Sharing across VMs is sequential, not concurrent.
- The project checkout lives on a volume; the agent clones into it. Getting
  files in/out of a volume goes through the guest agent (`onyx cp`-style),
  not through the host mounting the image.

Rejected: virtio-fs mount of a host folder (inotify gaps, perf, uid mapping,
and it drags in path identity — see D9); Mutagen-style sync (conflicts, lag).

## D9. Path identity: not a goal

Sessions and memory written inside a VM are **not** visible to `claude` on
the Mac. The earlier requirement was dropped once D8 removed host mounts.
Guest paths are whatever is natural for Linux (`/home/<user>/…`).

## D10. Agent state (`~/.claude`) lives on a volume

A `claude-state` volume holds `~/.claude/projects/` (sessions + memory) and
is attached to whichever VM is working; because attachment is exclusive
(D8), there is no concurrent-writer problem. Everything else:

| Path | Handling |
|---|---|
| `~/.claude/projects/` | on the state volume |
| `~/.claude/CLAUDE.md`, `settings.json`, plugins, skills | on the state volume, or seeded from the pack/config at boot |
| `~/.claude/.credentials.json` / OAuth token | **never on a volume** — delivered as a pack secret (D6) |
| `~/.claude.json` | seeded by the guest agent (trust/onboarding flags) |

## D11. Harness: Claude Code only in v1

Other agents (Codex, Gemini CLI, …) need per-agent adapters later.

## D12. UI: SwiftUI shell over a Go core

Go is the engine, exposed as plain HTTP+JSON over a Unix socket
(`internal/api`); the console is a hijacked connection carrying raw bytes.
Chosen over gRPC so a SwiftUI client needs `URLSession`, not codegen. The
CLI (`internal/client`) is the first client; SwiftUI is next. Linux UI
deferred.

## D13. Lifecycle: VMs die with the app

No daemon in v1. The Go core is structured as a service from day one so
moving it under launchd later is a packaging change, not a rewrite.

## D14. Session model

`onyx run` creates a VM for one interactive session: a work volume at
`/home/dev/work`, the shared `claude-state` volume at `/home/dev/.claude`,
packs delivered, then the serial console is attached to the user's
terminal. The guest's console login (`agetty -a dev` on hvc0) hands off to
whatever the host put in `/run/onyx/session` — the harness command and
working directory. When the command exits the VM is stopped and its
definition removed; volumes persist.

The serial console is the interactive channel (host pty ↔ virtio console).
Resize is pushed to the guest with `stty` since a serial line has no
SIGWINCH. Resource limits, sleep/resume across host sleep, and save/restore
are still open.

## D15. Distribution

Deferred. Ad-hoc signed local builds until the concept is proven.
