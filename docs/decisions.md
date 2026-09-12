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

## D3. Networking: NAT by default; restricted mode removes the NIC

`VZNATNetworkDeviceAttachment` by default (`network: nat`). Egress control
([issue #1](https://github.com/edwinavalos/onyx/issues/1)) is implemented
as `network: restricted`: the VM gets **no network device**, and the host
serves an HTTP forward proxy on a vsock port that admits only the VM's
`allow` list (`host`, `*.suffix`, `host:port`; 80/443 when no port). The
guest agent bridges it to `127.0.0.1:3128` and sets the standard proxy
variables (loopback excluded, so credential proxies stay direct).

Why a proxy and not a userspace netstack or pf: hostname allowlisting
falls out of `CONNECT` without terminating TLS or intercepting DNS; the
guest cannot bypass a NIC it does not have, so enforcement is host-only
(the guest is untrusted); and the credential proxies already established
the vsock-bridge pattern. The cost is that non-HTTP protocols (ssh, raw
TCP, UDP, ICMP) have no path at all in restricted mode — accepted, since
the same gap already exists for proxy-mode secrets (D6). Every decision
is logged as `allow|deny host:port` to `egress.log`; denials are how a
user grows the list. `network: none` is the same without the proxy.

## D4. Threat model: protect the host from the agent

Priority order: (1) host isolation — the VM boundary; (2) secrets not
leaking into transcripts — best effort via D6; (3) network — egress
allowlist per VM (D3).
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
- `proxy` — **implemented for HTTP(S) upstreams.** The guest never receives
  the value. The host runs a reverse proxy per proxy secret on a host vsock
  port, injecting `Authorization` (basic `x-access-token:<v>` by default,
  or bearer / arbitrary header); the guest agent bridges a loopback TCP
  port (stable per upstream, 40000–49999) to it and writes a git
  `url.<loopback>.insteadOf <upstream>` rewrite into `/run/onyx/gitconfig`.
  `git clone https://github.com/...` inside the VM just works, and the
  token never exists in the guest — verified by grepping tmpfs, home and
  every `/proc/*/environ`. Every proxied request is logged (method + path)
  to `proxy.log`. The agent can still *use* the credential (that is the
  point); it cannot read, print, or exfiltrate it.
  Claude Code's own credential goes through the same path: the guest
  profile sets `ANTHROPIC_BASE_URL` to the loopback proxy and a placeholder
  `CLAUDE_CODE_OAUTH_TOKEN`/`ANTHROPIC_API_KEY`; the proxy strips the
  placeholder and injects the real one. Verified with `claude -p` in-guest.
  Not covered: non-HTTP protocols (ssh), and cloud CLIs that sign requests
  client-side (AWS SigV4) — those need `credential_process`-style helpers.
  Gotcha found on the way: Vz's `removeSocketListenerForPort` never returns
  after the VM stops, so host listeners are abandoned, not closed, on reap.

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

Go is the engine, exposed as plain HTTP+JSON (`internal/api`) on a Unix
socket for the CLI and, when started with `-http`, on a loopback TCP port
guarded by a per-launch bearer token (`serve.json`) for the app — URLSession
cannot speak Unix sockets. The console is a hijacked connection carrying
raw bytes. Chosen over gRPC so the Swift client needs no codegen.

The macOS app (`app/`, SwiftPM + SwiftUI + SwiftTerm, bundled by `make app`
into `dist/Onyx.app` with the Go core as `Contents/MacOS/onyx-core`) launches
the core as a child with `-with-parent`, so VMs die with the app even on a
crash (D13). The CLI can drive the app's core over the socket at the same
time. Linux UI deferred.

## D12a. Agent access: MCP over stdio

`onyx mcp` (internal/mcpserver, official Go SDK) exposes the core to
coding harnesses. It is a peer of the CLI and the app over the same
HTTP/JSON API. Rule: secret values never cross MCP — key names and pack
definitions only — so an agent can wire packs into VMs but cannot read
what is in them. With no core running it spawns `serve -with-parent`, so
the harness session owns those VMs (D13 applied to agents).

## D13. Lifecycle: VMs die with the app

No daemon in v1. The Go core is structured as a service from day one so
moving it under launchd later is a packaging change, not a rewrite.

## D13a. The app attaches to a core it did not start

`onyx serve` always listens on a token-protected loopback TCP port too and
writes `serve.json`. On launch the app pings the core named there and
attaches if it answers (status "attached to a running core"; quitting the
app then leaves that core's VMs up, and the quit confirmation is skipped).
Otherwise it spawns its own core as before. This is what happens when an
`onyx mcp` session or a terminal `onyx serve` is already up; previously
the app's core refused the socket and the app showed only the exit status.
Core failures are shown with the tail of `serve.log`, selectable, with a
Copy button.

## D13b. Tests first; the core must never hold `c.mu` across a panic

A VM being started sits in `c.running` with a nil machine until Vz has
built it. `GetVM` dereferenced that and panicked with `c.mu` held, which
wedged every later request (the app could not list or delete anything).
Rules from that: every lookup of a running VM goes through `instance()`,
which refuses a starting VM; `GetVM` reports `starting` until `StartVM`
finishes; locks are released with `defer` wherever the critical section
calls anything that can panic. `stop` on a starting VM cancels the start
(the app's Cancel button): the agent wait is abandoned, the machine
stopped, the slot released. A guest that does not know an optional op
(`sshkey` on an older image) is a warning, not a failed start. Bugs get a
failing test before a fix
(`internal/core/starting_test.go`, `api_starting_test.go`); the app has a
SwiftPM test target (`make app-test`, part of `make ci`) for wire models
and pure helpers.

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
SIGWINCH.

**Sleep/resume.** Host sleep is a non-issue while `onyx serve` runs:
Virtualization freezes the VM with the process and it continues on wake.
`vm pause`/`vm resume` freeze explicitly.

**Suspend-to-disk is Vz save/restore** (`vm suspend`): pause → save →
stop; `vm start` restores → resume. The guest plays no part, so it works
for any image. The earlier "EINVAL for Linux guests" diagnosis was wrong:
Virtualization randomises the `VZGenericMachineIdentifier` per
configuration and a saved state only restores into one with the *same*
identifier; Onyx persists it per VM (`vms/<name>/machine-id.bin`). Guards:
a fingerprint of definition + machine id (mismatch → snapshot discarded,
cold boot), and a suspended VM's volumes cannot be attached elsewhere or
deleted. After restore the agent resets the guest wall clock from the host
and host-side proxy listeners are re-created. Guest hibernation (swsusp)
was built and worked but was dropped in favour of one mechanism that does
not depend on the guest OS. Limits are spelled out in
`docs/suspend-guide.md`. Resource limits remain open.

## D14a. ssh access rides vsock, not the network

`onyx ssh` (`ossh`) is OpenSSH with a `ProxyCommand` of `onyx vm dial
<vm> 4244`: the core opens a vsock stream to the guest agent, which bridges
it to sshd on `127.0.0.1:22`. sshd never listens on the NIC, so a NAT VM
exposes nothing to the LAN and restricted/none VMs (no NIC) are reachable
all the same. One ed25519 key per install, generated with `ssh-keygen` on
first use and written as the work user's *only* `authorized_keys` entry at
every start; host-key checking is off because the tunnel is host-local.
One-shot commands run under `bash -l` so they see `/run/onyx/env` like an
interactive login. `oclaude` is the same with `cd ~/work; onyx-trust; exec
claude "$@"` as the command.

## D15. Distribution

Deferred. Ad-hoc signed local builds until the concept is proven.
