# Onyx design questions

Answer inline under each `**Answer:**`. Questions marked ⚠ change the
architecture rather than the polish — answer those first.

## Virtualization

### 1. ⚠ How does Go talk to Virtualization.framework?
It's an Objective-C/Swift API. Options: cgo + Obj-C shims (`Code-Hex/vz`),
a separate Swift helper process Go talks to over a socket, or shell out to
`lima`/`tart`. This also shapes the UI answer (Q17). The framework needs a
signed binary with the `com.apple.security.virtualization` entitlement.

**Answer:**

### 2. What guest OS?
Vz runs Linux guests and macOS guests (Apple Silicon only, max 2 macOS
guests). If Linux: what's the boot story — kernel+initrd+raw disk, EFI
(`VZEFIBootLoader`), or both? QEMU wants qcow2; Vz wants raw. Convert, or
restrict?

**Answer:**

### 3. Name the gap QEMU fills.
Candidates: x86 guests on Apple Silicon, disk snapshots, nested virt, USB
passthrough. If you can't name one you need, drop QEMU from v1.

**Answer:**

### 4. Networking.
NAT is easy. Can the VM reach the host? Host reach the VM? VMs reach each
other? Do you want to block the agent's internet except an allowlist
(Anthropic API, package registries)? That needs a proxy or custom vmnet.

**Answer:**

## Secrets

### 5. ⚠ How does a secret physically enter the VM?
Env vars at boot (readable via `/proc/<pid>/environ`), a file on a
virtio-fs mount, a tmpfs written by a guest agent, or a vsock service the
guest asks at runtime (never at rest in the VM, revocable mid-session).

**Answer:**

### 6. ⚠ What stops the agent from reading the secret?
If `AWS_SECRET_ACCESS_KEY` is in the shell's env, `env` puts it in the
transcript. "Not in the transcript" means reachable by tools but not by the
model. Do you want a credential-proxy model (VM holds a placeholder token;
a host-side proxy — git credential helper, HTTP proxy injecting
`Authorization` — substitutes the real one) instead of copy-the-secret?
This changes what a "pack" is.

**Answer:**

### 7. Keychain access from a Go process.
Reading items prompts unless the ACL includes your app, and the ACL is tied
to code signature — every dev rebuild re-prompts. Data Protection Keychain
with an access group, or accept prompts?

**Answer:**

### 8. Pack scoping and lifecycle.
Pack attached to a VM, a project folder, or a session? Two projects in one
VM with different packs? Do running VMs pick up pack edits? Does deleting a
Keychain item revoke it in-VM?

**Answer:**

### 9. Audit.
Do you want to know which secrets a VM/session actually touched? Free with
vsock/proxy; impossible with env injection.

**Answer:**

## Shared folders and sync

### 10. Mount vs sync.
virtio-fs is a live mount: instant, but guest inotify doesn't fire for host
edits, `node_modules`-style trees are slow, uid/gid mapping needed.
Mutagen-style two-way sync fixes watchers and perf but adds conflicts and
lag. What does `npm install` inside the VM do to your host folder?

**Answer:**

### 11. Permissions and ownership.
Host files owned by uid 501 appear in the guest as what? If the guest runs
as uid 1000, can it write? virtio-fs on macOS doesn't fully honor guest
chmod.

**Answer:**

### 12. The `.git` directory.
Share it? If the agent commits in-VM, whose identity/signing key? Two VMs
on the same repo will corrupt each other's index. Worktrees per VM?

**Answer:**

## Agent memory and config sharing

### 13. ⚠ Path identity.
Claude Code keys projects by absolute path
(`~/.claude/projects/-Users-edwin-repos-onyx`). If the VM mounts at
`/workspace/onyx`, host `claude` won't find those sessions. Mount at the
same absolute path in the guest, rewrite paths in session files, or symlink?

**Answer:**

### 14. ⚠ What's actually in `~/.claude`?
Sessions, memory, settings, and credentials. Sharing the whole dir shares
your Anthropic auth token with every VM. Which sub-paths are shared, which
are per-VM, and which go through the pack mechanism?

**Answer:**

### 15. Concurrency.
Two VMs plus the host appending to the same JSONL sessions and `MEMORY.md`.
Claude Code doesn't lock these. Is last-writer-wins acceptable?

**Answer:**

### 16. Beyond Claude.
Codex, Cursor, Aider, Gemini CLI each have their own config dir and
path-keying. Per-agent adapter, or v1 is Claude Code only?

**Answer:**

## Product, UX, ops

### 17. UI stack.
SwiftUI for macOS (+ something else for Linux), or one cross-platform choice
(Wails, Fyne, Tauri+Go, TUI)? If Q1 already needs a Swift helper, SwiftUI
shell + Go daemon is natural. Menu-bar app, window app, or CLI with
optional GUI?

**Answer:**

### 18. ⚠ Daemon or not?
Vz VMs die when the creating process exits. If the GUI quits, do VMs die?
If not, you need a launchd daemon and the GUI/CLI talk to it.

**Answer:**

### 19. How does an agent session actually start?
SSH in? Embedded terminal? VM console? How does Onyx know a session started
— shell hook, or a wrapper binary named `claude`?

**Answer:**

### 20. Guest agent.
Secrets via vsock, mount setup, session detection, uid mapping all imply a
small Onyx agent in the guest. Conflicts with "bring your own image." Require
a base image, inject via cloud-init/ignition, or ship over virtio-fs and ask
the user to run it?

**Answer:**

### 21. ⚠ Threat model.
Protect the host from the agent (VM escape is the boundary), protect secrets
from the agent/transcript (Q6), or protect the network from the agent (Q4)?
Which one is the reason you'd use this over `docker run`?

**Answer:**

### 22. Resource limits and lifecycle.
CPU/RAM per VM, disk growth (raw is pre-allocated; sparse files help), what
"bring down" means (graceful via guest agent vs kill), save/restore state
across laptop sleep?

**Answer:**

### 23. Distribution.
Signed + notarized .app, Homebrew cask, `go install`? Given Q1's
entitlement, `go install` alone won't work.

**Answer:**
