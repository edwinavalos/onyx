# E2E test survey

What an end-to-end harness for Onyx should cover, and what each check
would look at. Written 2026-09-12 after the first dogfood session (seven
bugs found in one evening by using the product; none of them was covered
by a test).

## Status

Step 1 of the order below landed 2026-09-13: `e2e/` holds the harness
(`harness_test.go`: core on a temp root, image import, VM/exec helpers;
`screen_test.go`: console through hinshun/vt10x with `waitFor`/`waitText`
and a grid dump on failure or `ONYX_E2E_VERBOSE=1`) and one scenario per
primitive in `scenarios_test.go`. `make e2e` runs it in ~20 s; `make ci`
vets and lints it without running it. Observed while writing them: the
console login regularly beats the session request, so `onyx: waiting for
the host to finish setup...` is a normal line on the grid, not a defect —
the scenario asserts on what follows it instead.

Step 2 (console/session table) landed the same day in `console_test.go`:
resize before the session is up (Resize polled during a goroutine'd
start), resize while a program runs (a `sh -c` child with a WINCH trap —
bash's own trap only fires while it reads a line), detach/reattach with a
third concurrent attach (mirrored, never refused), exit → poweroff and
exit → shell, and console.log growth while detached. Suite is ~55 s. Not
covered: "session VM reaped" is the app's SessionReaper, not the core;
"Claude Code first run" needs a `claude` pack and network. Learned: the
session command is `eval`'d by the login shell, so a `Cmd` that ends in
a bare `exit` ends the login and agetty logs in and runs it again in a
loop — scenarios wrap such commands in `sh -c`.

## Primitives

Three observation points give everything below. Build them once.

1. **Core under test.** A Go test package (`e2e/`, build tag `e2e`) that
   starts `./bin/onyx serve` against a temp `ONYX_HOME`/`ONYX_SOCKET`,
   imports `images/out`, and drives it with `internal/client`. Needs the
   signed binary and a built image; skipped otherwise. Every scenario runs
   here, never against the user's state dir.
2. **Screen.** The console is a byte stream (`/v1/vms/{name}/console`,
   also `console.log`). Feed it through a VT emulator in the test (Go:
   `hinshun/vt10x` or `charmbracelet/x/vt`) and assert on the *rendered
   grid*: row N contains X, the cursor is on the last row, nothing below
   the prompt, no line appears twice. This is what a human sees and it is
   deterministic; the OpenRC-output scramble and the resize bug are
   grid-level facts, not byte-level ones. Feed the same stream after a
   resize with the new size to check reflow.
3. **Guest state.** `exec` (as root and as `dev` in a login shell) for
   files, listeners, env, processes; `console_log` for the raw stream;
   host-side `serve.log`, `audit.log`, `proxy.log`, `metrics.jsonl`.

The SwiftUI app is a fourth, more expensive point: System Events
accessibility plus screenshots today, XCUITest when there is an Xcode
project. Keep it for the handful of app-only behaviours at the end; most
"UI" bugs so far were terminal bugs and are testable without the app.

## Scenarios

### Console and session (screen-level)

| Scenario | Assert | History |
| --- | --- | --- |
| Boot to the session command | Screen shows the banner, then the command's first output; no `onyx: waiting for the host` left over; no OpenRC service lines anywhere on the grid | 8c0c94a: default-runlevel output scrambled Claude Code |
| Resize before the agent is up | Session starts with the size the terminal reported (`stty size` from the session shell = rows/cols sent) | dc5fa17: resize-during-start |
| Resize while a full-screen program runs | After `Resize`, the guest tty reports the new size (`winsize` op) and the emulated grid at the new size has the prompt on the last row, no duplicated status line | the "resize bug" |
| Detach and reattach | Ctrl-] leaves the VM running; a second console attach replays the tail and new output continues; a third concurrent attach is refused or mirrored, but never corrupts either | |
| Session exit → poweroff | `ONYX_SESSION_EXIT=poweroff`: command exits, "powering off" printed, VM reaches stopped, session VM reaped, work volume kept | dc5fa17: session VMs reaped only after they ran |
| Session exit → shell | Non-poweroff: prompt appears within 1 s of the exit line | |
| Console output while nobody is attached | console.log keeps growing; the core's offset is right after reattach (no empty log) | never truncate console.log while running |
| Claude Code first run | With pack `claude`, the first screen after boot is Claude's prompt, not the trust/onboarding dialog | dfa2456: wrapper pre-accepts |

### VM lifecycle (API-level)

| Scenario | Assert |
| --- | --- |
| create/start/stop/rm | states in order, `started` set, root disk removed on rm, volumes kept |
| start twice, stop while starting | "already starting/running"; Cancel path leaves no `starting` ghost in `list_vms`; core stays responsive (every call under a 5 s timeout) — the wedged-mutex family |
| pause/resume | exec fails while paused, works after; console stream resumes |
| suspend/resume | a counter process in the guest continues from its value; clock is resynced (guest `date` within 2 s of host); machine-id restored (resume succeeds at all) |
| suspend then change the definition | start refused with the fingerprint message |
| volume held by a running VM | second start refused naming the holder (97f61b7); held by a suspended VM likewise |
| kill the core with VMs running | on restart: VMs listed stopped, no stale `serve.json`, sockets cleaned; the app attaches rather than "core exited (status 1)" |
| metrics | every start appends one metrics.jsonl line; `guest_boot_ms` under a budget (baseline 0.5 s) so a regression is a failing test, not a feeling |

### Secrets, packs, proxies

| Scenario | Assert |
| --- | --- |
| env/file modes | value in `/run/onyx/env` (tmpfs only: not on any block device), file at path with the requested mode; audit.log line per secret |
| proxy mode | guest env has `ONYX_PROXY_*` and a loopback listener; a request through it reaches upstream with the real credential and the placeholder header stripped (a local httptest upstream on the host in place of api.anthropic.com); proxy.log line; the value never appears in `console.log`, `env`, or any exec output |
| redeliver to a running VM | idempotent (86c3b2e); after an `onyx-guest` restart env and bridges are back |
| rotated secret | change the Keychain item; within 30 s the proxy sends the new value |
| linked secret (`secret link`) | resolves through to the other Keychain item; never copied |
| missing secret at start | start fails early with the pack/key named, VM not left running |
| restricted network | no `eth0`; allow-listed host reachable via the egress proxy, another host refused; Claude Code answers through the credential proxy (verified by hand 2026-09-12) |
| network none | nothing reachable, proxies still work |

### Guest agent

| Scenario | Assert |
| --- | --- |
| exec semantics | exit code, combined output, root vs `dev` login shell (PATH has /usr/local/bin, pack env present — d40889f/loginArgv) |
| long exec | a 20 s command does not block a concurrent `get_vm`/stop (fresh connection per call) |
| copy in/out | ownership `dev`, modes and symlinks preserved, a directory round-trips byte-identical |
| volumes | first mount formats ext4, second boot keeps data, two volumes get `/dev/vdb`,`/dev/vdc` in definition order |
| agent restart / hot swap | `make guest-swap` leaves exec working and packs redelivered |
| old image | an image whose agent lacks an op (ssh key) degrades with the warning, start still succeeds |

### ssh over vsock

| Scenario | Assert |
| --- | --- |
| `ossh vm` right after start | connects within the 15 s retry window (sshd is ~3 s behind the agent) |
| `ossh vm cmd` | runs in a login shell: pack env and proxies present |
| `oclaude vm` | Claude Code starts in `~/work` with the proxy env |
| key handling | per-install key is the only authorized key after every start; a key planted in the guest is gone after restart |
| ssh into stopped/suspended VM | starts/resumes it first |

### MCP

| Scenario | Assert |
| --- | --- |
| schema | every tool's output schema is an object; text tools carry `output` (36baf2d, d40889f) — already unit-tested |
| one call per tool | over stdio against the test core: list/get/create/start/exec/copy/console_log/stop/remove/volumes/packs/secret keys — no tool returns `{}` where it has something to say |
| errors | a failing exec is `isError` with the output attached; unknown VM is a clean error, not a hang |
| secret values | `list_packs`/`list_secret_keys` never include a value; grep the whole MCP transcript |

### App (accessibility-driven, later XCUITest)

| Scenario | Assert |
| --- | --- |
| launch attaches to a running core | status item shows connected; no second core spawned |
| New VM defaults | pack `claude`, `claude-state` attached, 1 vCPU/512 MB |
| start from the sidebar → terminal | the SwiftTerm view shows the same grid the VT emulator computed from console.log (screenshot OCR is not needed: compare SwiftTerm's buffer via an accessibility value or a debug export) |
| resize the window during boot | same assertion as the console resize scenarios, through the app |
| failure view | a start failure is shown copyable; Cancel on a starting VM works |
| volumes page | multi-select delete; a volume attached to a running VM is refused with the holder named |
| quit with VMs running | VMs stopped or suspended per setting; nothing orphaned |

## Order

1. Primitives 1–3 with one scenario each (boot to prompt on the screen,
   create/start/stop, env-mode secret). This is the harness.
2. Console/session table: the resize and scramble cases, since they are
   the ones that were found by eye.
3. Secrets/proxy table with an httptest upstream: the security claims of
   the README become tests.
4. Lifecycle edge cases (starting/cancel/kill), then ssh, then MCP over
   stdio.
5. App scenarios last, and only the ones the console harness cannot see.

Run as `make e2e` (tag `e2e`, serial, ~2–3 min at 1 s per boot); not part
of `make ci`.
