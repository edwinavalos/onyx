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

## D8. Shared folders: virtio-fs live mount, no sync layer

`VZVirtioFileSystemDeviceConfiguration`. Known rough edges (inotify from host
edits, `node_modules` perf, chmod semantics) are accepted; we test against a
`/tmp` folder first and learn. Because the VM sees the real working tree,
**one VM per project folder** — no `.git` sharing between VMs. (rubbish's
bare-cache + per-session clone model is a different product and is not
adopted.)

## D9. Path identity: mirror host paths in the guest (provisional)

The guest mounts the project at the **same absolute path** as the host
(`/Users/edwin/repos/onyx` → `/Users/edwin/repos/onyx`). The guest agent
creates the working user with the **host's uid and home directory path**.
Result: Claude Code's project key, `~/.claude.json` project entries, and
`cwd` in session JSONL are identical on both sides with no translation, and
virtio-fs ownership lines up.

Rejected: canonical guest path + key translation (fragile shim over
Claude Code's on-disk format); per-VM isolated `~/.claude` (doesn't deliver
the feature). See `design-questions.md` Q13 discussion.

## D10. What is shared from `~/.claude`

Following rubbish's split:

| Path | Handling |
|---|---|
| `~/.claude/projects/` (sessions + per-project memory) | virtio-fs shared, read/write |
| `~/.claude/CLAUDE.md` | copied into guest at boot (static) |
| `~/.claude/settings.json`, plugins, skills | per-VM (host hooks may not apply in guest) |
| `~/.claude/.credentials.json` / OAuth token | **never shared** — delivered as a pack secret (D6) |
| `~/.claude.json` | per-VM; agent seeds trust/onboarding flags for the mounted project |

Concurrency: each session writes its own JSONL, so contention is limited to
memory files. Last-writer-wins accepted. Claude Code has no hook on transcript
or memory writes, so locking via hooks is not possible.

## D11. Harness: Claude Code only in v1

Other agents (Codex, Gemini CLI, …) need per-agent adapters later.

## D12. UI: SwiftUI shell over a Go core

Go is the engine, exposed over a local API (Unix socket; protocol TBD, gRPC
likely given rubbish precedent). SwiftUI is a client. Linux UI deferred.

## D13. Lifecycle: VMs die with the app

No daemon in v1. The Go core is structured as a service from day one so
moving it under launchd later is a packaging change, not a rewrite.

## D14. Session model

`onyx` launches the user's harness inside a fresh VM scoped to that session.
Additional folders come from a per-VM config file. Resource limits, sleep/
resume across host sleep, and graceful shutdown semantics are open — needs
research (Vz supports save/restore of full VM state on Apple Silicon).

## D15. Distribution

Deferred. Ad-hoc signed local builds until the concept is proven.
