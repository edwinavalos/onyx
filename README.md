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
./bin/onyx secret link claude-token -claude-code   # use the host's Claude Code login, resolved live
./bin/onyx pack create claude -secret 'claude-token>https://api.anthropic.com>bearer'
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
onyx vm suspend dev      # save memory+device state on the host; `vm start` resumes it (docs/suspend-guide.md)
onyx vm stop dev
```

Secrets are written only to tmpfs inside the guest (`/run/onyx/`); every
delivery is recorded by name in `~/Library/Application Support/Onyx/audit.log`.

For HTTP upstreams you can keep the secret on the host entirely:

```sh
gh auth token | onyx secret set gh-token
onyx pack create gh -secret 'gh-token>https://github.com'
onyx run -pack gh          # git clone https://github.com/you/private works;
                           # the token never enters the VM. Requests are logged
                           # to ~/Library/Application Support/Onyx/proxy.log
```

The same works for Claude Code's own credential, so the agent never holds
its own API token either — inside the VM it only sees a placeholder and
`ANTHROPIC_BASE_URL` pointing at the loopback proxy:

```sh
onyx secret link claude-token -claude-code    # follows the host's Claude Code OAuth token as it refreshes
onyx pack create claude -secret 'claude-token>https://api.anthropic.com>bearer'
# alternatives: `onyx secret set claude-token` with a token from `claude setup-token`,
# or an API key: -secret 'anthropic-key>https://api.anthropic.com>header:x-api-key'
onyx run -pack claude
```

`secret link` never copies the value: it is read from the other Keychain
item each time it is needed (`-service`/`-account`/`-json` work for any
app's item). The proxy re-reads secrets every 30 s, so rotated tokens are
picked up while a VM is running.

### Network egress

By default a VM sits behind Virtualization's NAT with full internet. A
`restricted` VM gets **no NIC at all**: its only way out is a host-side
HTTP(S) proxy over vsock that admits the hosts you list and refuses
everything else (hostname-matched at `CONNECT`, no TLS termination). The
guest's `HTTP_PROXY`/`HTTPS_PROXY` point at it, so curl, git, apk, npm, pip,
go and Claude Code just work; anything that ignores the proxy simply has no
network. Credential proxies are vsock too, so they keep working.

```sh
onyx run -pack claude -network restricted \
    -allow github.com -allow '*.githubusercontent.com' -allow registry.npmjs.org
# entries: host, *.suffix (strict subdomains), host:port (80/443 when omitted)
tail -f ~/Library/Application\ Support/Onyx/egress.log   # every allow/deny, host:port only
onyx vm create dark -network none                          # no NIC, no proxy
```

## macOS app

```sh
make app-run      # builds dist/Onyx.app (SwiftUI + embedded terminal) and opens it
```

The app starts its own core; the sidebar lists VMs, volumes, images, packs
and Keychain secrets, and a running VM's serial console is embedded in the
detail pane. "Run Session" does what `onyx run` does. The CLI keeps working
against the app's core while it is open. Requires Xcode (SwiftPM + SwiftUI).

## Driving Onyx from a coding agent (MCP)

`onyx mcp` serves the Model Context Protocol on stdio, so any harness that
supports MCP can manage sandboxes directly: list/create/start/stop VMs,
`exec` inside them, read the console log, start sessions, copy files, and
inspect volumes, packs and secret *key names*. Secret values are never
exposed through MCP. If no core is running it starts one that lives as long
as the harness session (its VMs stop when the harness exits).

```sh
make mcp-install                                   # Claude Code, user scope
claude mcp add onyx -s user -- "$PWD/bin/onyx" mcp # same thing by hand
codex mcp add onyx -- "$PWD/bin/onyx" mcp          # OpenAI Codex CLI
```

Cursor / other clients (`mcp.json`):

```json
{ "mcpServers": { "onyx": { "command": "/path/to/onyx/bin/onyx", "args": ["mcp"] } } }
```

## Development

```sh
make ci       # fmt, vet, lint, staticcheck, tests, gosec, govulncheck
make spike    # boot images/out end to end without the core, log to spike-console.log
```

State lives under `~/Library/Application Support/Onyx` (override with
`ONYX_HOME`; the API socket with `ONYX_SOCKET` — macOS caps socket paths at
104 bytes).
