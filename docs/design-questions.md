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
I think we should use cgo and shims, I think it's the harder path but with an LLMs help I think it makes sense to generate those.

### 2. What guest OS?
Vz runs Linux guests and macOS guests (Apple Silicon only, max 2 macOS
guests). If Linux: what's the boot story — kernel+initrd+raw disk, EFI
(`VZEFIBootLoader`), or both? QEMU wants qcow2; Vz wants raw. Convert, or
restrict?

**Answer:**
kernel+initrd+rawdisk. I think the @../rubbish might have this already setup in a small form, maybe we can lift some of that work, that work is a for something that runs firecracker, your terms are ringing a bell about our architecture choices there.


### 3. Name the gap QEMU fills.
Candidates: x86 guests on Apple Silicon, disk snapshots, nested virt, USB
passthrough. If you can't name one you need, drop QEMU from v1.

**Answer:**
You know ideally we would be able to do nested virt but we are so far down the rabbit hole at that point that I you are right to drop the qemu stuff.


### 4. Networking.
NAT is easy. Can the VM reach the host? Host reach the VM? VMs reach each
other? Do you want to block the agent's internet except an allowlist
(Anthropic API, package registries)? That needs a proxy or custom vmnet.

**Answer:**
This should become a future feature, as far as security features, make sure we make a GitHub issue to track it.

## Secrets

### 5. ⚠ How does a secret physically enter the VM?
Env vars at boot (readable via `/proc/<pid>/environ`), a file on a
virtio-fs mount, a tmpfs written by a guest agent, or a vsock service the
guest asks at runtime (never at rest in the VM, revocable mid-session).

**Answer:**
If it was a vsock service the vmd would need to have an agent installed? Or is it much better nowadays?

### 6. ⚠ What stops the agent from reading the secret?
If `AWS_SECRET_ACCESS_KEY` is in the shell's env, `env` puts it in the
transcript. "Not in the transcript" means reachable by tools but not by the
model. Do you want a credential-proxy model (VM holds a placeholder token;
a host-side proxy — git credential helper, HTTP proxy injecting
`Authorization` — substitutes the real one) instead of copy-the-secret?
This changes what a "pack" is.

**Answer:**
I was thinking trying to integrate with a keyring thing of some sort, but then that requires people to install an agent like service on their own disk images, which kind of just defeats a lot of the versatility I want to have with this project as far as bringing your own disk image/vm image to boot. I might need to talk about this one more in depth with you.

### 7. Keychain access from a Go process.
Reading items prompts unless the ACL includes your app, and the ACL is tied
to code signature — every dev rebuild re-prompts. Data Protection Keychain
with an access group, or accept prompts?

**Answer:**
Would it prompt per access, or would it only prompt once for the life the pid?

### 8. Pack scoping and lifecycle.
Pack attached to a VM, a project folder, or a session? Two projects in one
VM with different packs? Do running VMs pick up pack edits? Does deleting a
Keychain item revoke it in-VM?

**Answer:**
I think a lot of the cool stuff I want to do is going to be dependent on some kind of agent service running listeners for events from this tool. I think I accept that will be the reality if I want this thing to work really well.


### 9. Audit.
Do you want to know which secrets a VM/session actually touched? Free with
vsock/proxy; impossible with env injection.

**Answer:**
Tell me more about the secrets management we could do with the vsock stuff.

## Shared folders and sync

### 10. Mount vs sync.
virtio-fs is a live mount: instant, but guest inotify doesn't fire for host
edits, `node_modules`-style trees are slow, uid/gid mapping needed.
Mutagen-style two-way sync fixes watchers and perf but adds conflicts and
lag. What does `npm install` inside the VM do to your host folder?

**Answer:**
We are gonna yolo this feature and see how badly it goes, only way to learn is to the wrong thing.

### 11. Permissions and ownership.
Host files owned by uid 501 appear in the guest as what? If the guest runs
as uid 1000, can it write? virtio-fs on macOS doesn't fully honor guest
chmod.

**Answer:**
See previous answer about yoloing it. I think for now I'll have it map to my /tmp/ folder on the Mac OS X and then see how badly it goes.

### 12. The `.git` directory.
Share it? If the agent commits in-VM, whose identity/signing key? Two VMs
on the same repo will corrupt each other's index. Worktrees per VM?

**Answer:**
I kind of figured this out in the rubbish program and it went fine/ok. I think we can learn what we did over there and bring in here but the difference will be apple virtualization vs firecracker. One sticky thing with the rubbish code, we have a copy of it at ../ but I don't recall if that work should be pushed or not (the git status was not clean, but I didn't examine specifics). So make sure that if the code that is needs to get pushed upstream or not.

## Agent memory and config sharing

### 13. ⚠ Path identity.
Claude Code keys projects by absolute path
(`~/.claude/projects/-Users-edwin-repos-onyx`). If the VM mounts at
`/workspace/onyx`, host `claude` won't find those sessions. Mount at the
same absolute path in the guest, rewrite paths in session files, or symlink?

**Answer:**
I think rubbish answered this somehow. Look at that solution. I think what I'd like to ultimately do is maybe inside onyx we have a way to view a file system that is just a workspace folder/volume presented as a folder where all the Claudes work from, and you could see them acting on the directories. I'm a bit stoned so this seems like a cool visualization but we can go for a simple file browser for the first version.

### 14. ⚠ What's actually in `~/.claude`?
Sessions, memory, settings, and credentials. Sharing the whole dir shares
your Anthropic auth token with every VM. Which sub-paths are shared, which
are per-VM, and which go through the pack mechanism?

**Answer:**
Rubbish had some solution for this, investigate it.

### 15. Concurrency.
Two VMs plus the host appending to the same JSONL sessions and `MEMORY.md`.
Claude Code doesn't lock these. Is last-writer-wins acceptable?

**Answer:**
I have no idea, I haven't used memory across agents like this, can you see if there a pre or post tool hooks in claude and other 'major' harnesses that could be used to gather a lock on the file, do you think we're able to use a third party memory provider that hooks officially into the SOTA harnesses? This one is weird and much more tied to the idea that this is a whole agent platform rather than a tool that is used to manage the VMs. Maybe we're looking to build a vm first coding harness, a lot of these questions might be discussed online, can you do some research.

### 16. Beyond Claude.
Codex, Cursor, Aider, Gemini CLI each have their own config dir and
path-keying. Per-agent adapter, or v1 is Claude Code only?

**Answer:**
Yes for now

## Product, UX, ops

### 17. UI stack.
SwiftUI for macOS (+ something else for Linux), or one cross-platform choice
(Wails, Fyne, Tauri+Go, TUI)? If Q1 already needs a Swift helper, SwiftUI
shell + Go daemon is natural. Menu-bar app, window app, or CLI with
optional GUI?

**Answer:**
Let's do the hard thing, let's do swift UI for now and not worry too much about the linux interface yet. We will learn a lot while building the swiftui I imagine if there's a similar framework out in open source world.

### 18. ⚠ Daemon or not?
Vz VMs die when the creating process exits. If the GUI quits, do VMs die?
If not, you need a launchd daemon and the GUI/CLI talk to it.

**Answer:**
Let them die for now.

### 19. How does an agent session actually start?
SSH in? Embedded terminal? VM console? How does Onyx know a session started
— shell hook, or a wrapper binary named `claude`?

**Answer:**
`onyx` would open the users preferred coding harness inside a new vm that was created for the session length. If they need access to more folders that will have to be configured in a vm creation configuration file/settings.

### 20. Guest agent.
Secrets via vsock, mount setup, session detection, uid mapping all imply a
small Onyx agent in the guest. Conflicts with "bring your own image." Require
a base image, inject via cloud-init/ignition, or ship over virtio-fs and ask
the user to run it?

**Answer:**
I guess I was right about this, for now let's just go whole hog with an agent, we can figure out the portability problem when I want to expand this idea.

### 21. ⚠ Threat model.
Protect the host from the agent (VM escape is the boundary), protect secrets
from the agent/transcript (Q6), or protect the network from the agent (Q4)?
Which one is the reason you'd use this over `docker run`?

**Answer:**
Protect the host from the agent is the most important one for me right now.
If we can come up with a safe non leaking solution from the agent I would like to do that, I think this is one of those incredibly hard problems that requires a lot of tradeoffs because we are running in claude in userspace.

### 22. Resource limits and lifecycle.
CPU/RAM per VM, disk growth (raw is pre-allocated; sparse files help), what
"bring down" means (graceful via guest agent vs kill), save/restore state
across laptop sleep?

**Answer:**
This is where I know you are asking about features that aren't available in certain hypervisors. Here I don't really care, but ideally the vm would also sleep when the users laptop slept and we could be able to resume it when we next needed it, or when the host woke up. We'll need to do more research on this one.

### 23. Distribution.
Signed + notarized .app, Homebrew cask, `go install`? Given Q1's
entitlement, `go install` alone won't work.

**Answer:**
We'll deal with the repercussions until we prove this can work.
