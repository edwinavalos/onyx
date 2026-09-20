# Onyx

Onyx runs coding agents (Claude Code, Codex, Pi) inside local virtual
machines on macOS. Each VM is a sandbox with the plumbing done for you:
secrets come from the macOS Keychain and are delivered at boot, project
folders live on volumes you can attach to any VM, and agent state is kept
on a shared volume so a project's memory follows it from one VM to the
next. Credentials can be proxied on the host so the token never enters the
guest at all.

It is a Go core driving Apple's Virtualization.framework, a CLI, a SwiftUI
app with an embedded terminal, and an MCP server so an agent can manage its
own sandboxes. Apple Silicon only.

## Requirements

- macOS 13 or later on Apple Silicon
- Go 1.26+
- Docker, to build the guest images
- Xcode, only for the SwiftUI app

## Quick start

```sh
make tools        # pinned lint and security tools into ./bin
make images       # Alpine guest images for claude, codex and pi -> images/out/<agent>/
make sign         # build ./bin/onyx and ad-hoc sign it with the virtualization entitlement

./bin/onyx serve  # terminal 1: the core. VMs live as long as this runs.
```

In a second terminal:

```sh
./bin/onyx image import claude images/out/claude
./bin/onyx secret link claude-token -claude-code
./bin/onyx pack create claude -secret 'claude-token>https://api.anthropic.com>bearer'
./bin/onyx run -pack claude
```

`onyx run` creates a work volume, boots a fresh VM, delivers the pack and
attaches your terminal to the console with Claude Code already running.
Ctrl-] detaches and leaves the VM up. When the agent exits the VM is
stopped and removed; volumes persist.

`secret link` does not copy the token. Onyx reads the host's Claude Code
login from its own Keychain item each time it is needed, so it follows
refreshes. `secret set` stores a value you paste instead (for example from
`claude setup-token`), and an API key works too:

```sh
onyx pack create claude -secret 'anthropic-key>https://api.anthropic.com>header:x-api-key'
```

## Secrets and packs

Secrets are Keychain items. A pack is a named bundle of secrets plus how
each one is delivered to a VM. Three delivery modes:

| Entry | Delivered as |
|---|---|
| `name` | environment variable `NAME` |
| `name@/run/onyx/path:0600` | file at that path, with that mode |
| `name>https://host[>bearer\|header:X]` | host-side proxy; the guest sees a placeholder |

Env and file secrets are written only to tmpfs inside the guest under
`/run/onyx/`. Proxy secrets never leave the host: the guest's traffic to
that origin goes through a loopback proxy over vsock, and the host injects
the credential. With `>bearer` or `>header:` the proxy terminates TLS for
that origin (guests trust a per-install Onyx CA) and sets the header. The
proxy re-reads secrets every 30 seconds, so rotated tokens are picked up
while a VM is running.

GitHub is the usual example. `gh` and git both work, and `gh auth token`
inside the VM prints a placeholder:

```sh
gh auth token | onyx secret set gh-token
onyx pack create gh \
  -secret 'gh-token>https://github.com' \
  -secret 'gh-token>https://api.github.com>bearer'
onyx run -pack gh
```

Packs are editable. `pack edit` replaces every entry while keeping the
name, so VMs that reference the pack are unaffected; `-clear` empties it.
The app's Packs view edits individual entries. Every delivery is recorded by
secret name in `~/Library/Application Support/Onyx/audit.log`, and proxied
requests in `proxy.log`.

## VMs, volumes and files

Everything `onyx run` does is available piecemeal:

```sh
onyx vm create dev -volume work:/home/dev/work/work -pack claude
onyx vm start dev
onyx vm exec dev -- sh -c 'echo $CLAUDE_CODE_OAUTH_TOKEN | wc -c'
onyx vm console dev
onyx cp ./myproject dev:/home/dev/work/work      # into the VM, owned by dev
onyx cp dev:/home/dev/work/work/myproject ./out  # back out
onyx vm suspend dev   # save memory and device state; `vm start` resumes
onyx vm stop dev
```

Work volumes mount under `/home/dev/work/<volume>`. Claude Code keys its
memory by working directory, so each project keeps its own memory; `onyx run
-work <volume>` reuses a project's volume and `-dir` overrides the start
directory. Agent state (`~/.claude`, `~/.pi`) lives on a shared state volume
so sessions and memory carry over between VMs.

Inside the VM Claude Code runs in bypass-permissions mode by default: the
VM is the sandbox, so it does not stop to ask. Set `permissions.defaultMode`
in `~/.claude/settings.json` on the state volume to change that.

`onyx metrics` prints per-start phase timings. A fresh VM reaches its agent
in about half a second.

## SSH from any terminal

```sh
ossh dev                  # shell in the VM; starts it if stopped or suspended
ossh dev 'git status'     # one-shot command, with the delivered secrets in its environment
oclaude dev               # ssh in and launch Claude Code in ~/work
oclaude dev -p 'summarise the repo'
ocodex dev
opi dev
```

`ossh`, `oclaude`, `ocodex` and `opi` are symlinks to `onyx ssh` and
friends, installed by `make install`. The connection is real OpenSSH but
rides a vsock tunnel through the core rather than the network. The guest's
sshd listens on loopback only, so this works in every network mode. Auth is
a per-install ed25519 key in `~/Library/Application Support/Onyx/ssh/`,
installed as the work user's only authorized key at every start.

## Network egress

By default a VM sits behind Virtualization's NAT with full internet access.
A `restricted` VM has no NIC. Its only way out is a host-side HTTP(S) proxy
over vsock that admits the hosts you list and refuses everything else
(hostname-matched at `CONNECT`, no TLS termination). `HTTP_PROXY` and
`HTTPS_PROXY` point at it in the guest, so curl, git, apk, npm, pip, go and
the agents work; anything that ignores the proxy has no network.

```sh
onyx run -pack claude -network restricted \
    -allow github.com -allow '*.githubusercontent.com' -allow registry.npmjs.org
tail -f ~/Library/Application\ Support/Onyx/egress.log   # every allow and deny
onyx vm create dark -network none                          # no NIC, no proxy
```

Allow entries are `host`, `*.suffix` (strict subdomains) or `host:port`;
80 and 443 are implied when the port is omitted. Credential proxies are
vsock too, so they keep working in restricted mode.

## Agents

`make images` builds one image per agent. Each contains the guest tooling,
a development toolchain and exactly that agent's CLI. The agent picker in the
app, and `-agent` on the CLI and MCP, select the matching image and default
pack; `-image` overrides it for a bring-your-own image.

### Claude Code

Covered above. Its state volume is `claude-state`, mounted at
`/home/dev/.claude`.

### Codex

Codex keeps no state on a volume: `CODEX_HOME` is `/run/onyx/codex`
(tmpfs). Log in once with the device flow, store the resulting `auth.json`
in the Keychain, and a file-secret pack restores it at every boot:

```sh
onyx run -agent codex -cmd 'codex login --device-auth'
# open the URL on the host, enter the code, then import
# /run/onyx/codex/auth.json into secret codex-auth and:
onyx pack create codex -secret 'codex-auth@/run/onyx/codex/auth.json:0600'
onyx run -agent codex -pack codex
```

This subscription login is not proxied: code running in the Codex VM can
read `auth.json` while the VM is up. Do not attach the `codex` pack to other
agents' VMs. API-key packs work for API-billed use.

### Pi

Pi's state volume is `pi-state` at `/home/dev/.pi`, which holds provider
logins, sessions, settings and installed packages, so attach it only to VMs
you trust. Use Pi's `/login` or deliver a provider API key as an ordinary
secret:

```sh
onyx run -agent pi
```

## macOS app

```sh
make app-run   # builds dist/Onyx.app and opens it
```

The app runs its own core. The sidebar lists VMs, volumes, images, packs and
Keychain secrets; a running VM's console is embedded in the detail pane.
Run Session does what `onyx run` does. The CLI keeps working against the
app's core while it is open.

## Driving Onyx from an agent (MCP)

`onyx mcp` serves the Model Context Protocol on stdio: list, create, start
and stop VMs, `exec` in them, read the console, start sessions, copy files,
and inspect volumes, packs and secret key names. Secret values are never
exposed over MCP. If no core is running it starts one that lives as long as
the agent session.

```sh
make mcp-install                                    # Claude Code, user scope
claude mcp add onyx -s user -- "$PWD/bin/onyx" mcp  # the same by hand
codex mcp add onyx -- "$PWD/bin/onyx" mcp
```

For Cursor and other clients (`mcp.json`):

```json
{ "mcpServers": { "onyx": { "command": "/path/to/onyx/bin/onyx", "args": ["mcp"] } } }
```

## Development

```sh
make ci     # tidy check, fmt, vet, lint, staticcheck, tests, gosec, govulncheck, app tests
make e2e    # end-to-end suite against a real core and real VMs (about a minute)
make image  # faster Claude-only guest image build into images/out/
make guest-swap VM=<name>   # cross-compile the guest agent and hot-swap it into a running VM
```

State lives under `~/Library/Application Support/Onyx`. `ONYX_HOME` moves
it and `ONYX_SOCKET` moves the API socket (macOS caps socket paths at 104
bytes), which is how you run a scratch core next to the app's.

Layout: `cmd/onyx` (CLI, `serve`, `mcp`), `cmd/onyx-guest` (the agent that
runs inside the guest), `internal/core` (VM lifecycle, packs, sessions),
`internal/vm` (Virtualization.framework wrapper), `internal/proxy` (the
credential proxy), `internal/guest` and `internal/vsockproto` (guest side
and wire protocol), `images/` (Alpine image build and rootfs overlay),
`app/` (SwiftUI), `e2e/` (end-to-end harness).

Design decisions are numbered in `docs/decisions.md`; `docs/suspend-guide.md`
covers suspend and resume, and `docs/design-questions.md` the open
questions.

## License

MIT. See `LICENSE`.
