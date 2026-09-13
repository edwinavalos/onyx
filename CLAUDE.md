# Onyx

Local VM sandboxes for coding agents on macOS: a Go core boots Alpine guests
through Apple Virtualization.framework (Code-Hex/vz), attaches Onyx-managed
volumes, delivers Keychain secrets over vsock (or proxies them so the token
never enters the guest), and runs Claude Code on the console. A SwiftUI app,
a CLI and an MCP server (`onyx mcp`) all drive the same HTTP/JSON core.

## Layout

- `cmd/onyx` — CLI + `serve` + `mcp`; `cmd/onyx-guest` — the guest agent (Linux).
- `internal/core` — VM lifecycle, packs, proxies, sessions (host only).
- `internal/api`, `internal/client` — HTTP/JSON API on a Unix socket / loopback TCP.
- `internal/vm` — vz wrapper; `internal/guest` — agent logic; `internal/vsockproto` — wire protocol.
- `internal/store` — state dir, VM defs, `DefaultCPUs`/`DefaultMemoryMB`; `internal/volume` — raw disk images.
- `internal/pack`, `internal/keychain` — secret packs and macOS Keychain; `internal/mcpserver`; `internal/tarfs` (cp).
- `app/` — SwiftPM SwiftUI app + SwiftTerm; `images/` — Alpine image build + rootfs overlay.
- `docs/` — `decisions.md` (D1–D17), `suspend-guide.md`, `e2e-test-survey.md`, `design-questions.md`.
- `e2e/` — end-to-end harness (issue #4): real core on a temp root, VT-emulated console; `make e2e` (~20 s, not part of `make ci`). Add scenarios from `docs/e2e-test-survey.md`.

Linux-buildable packages: guest, keychain, pack, store, tarfs, volume,
vsockproto, cmd/onyx-guest. api/client/core/mcpserver/vm pull in vz (host only).

## Build and test

- `make build` → `./bin/onyx`; `make sign` — ad-hoc sign with the virtualization
  entitlement (needed to launch VMs); `make tools` — pinned linters into ./bin.
- `make ci` = tidy-check + fmt-check/vet/lint/staticcheck/test/overlay-test + sec + app-test.
  **Must be green before an issue is done.** `make test` alone runs Go tests with -race.
- `make image` (Docker) builds the guest image into `images/out/`. The guest
  agent lives in the image: after protocol/overlay changes rebuild and re-import
  with an **absolute path** (the core may be running inside the app):
  `onyx image rm base && onyx image import base "$PWD/images/out"`.
- `make guest-swap VM=<name>` — cross-compile onyx-guest and hot-swap it into a
  running VM (seconds; no image rebuild). `make overlay-test` — host-side tests
  of the overlay scripts (needs node).
- `make app` → `dist/Onyx.app`; `make app-run` pkills and relaunches it;
  `make app-test` — SwiftPM unit tests. `make mcp-install` registers the MCP server.
- `onyx metrics` tabulates per-start phase timings from `<root>/metrics.jsonl`.
- **Scratch core for manual testing**, never the real state dir while the app
  is up (`pgrep -x Onyx` first):
  `ONYX_HOME=<scratch> ONYX_SOCKET=/tmp/onyx-t.sock ./bin/onyx serve &`
  (socket paths cap at 104 bytes), then `image import`, `pack create`,
  `vm create/start` against it; `pkill -f "bin/onyx serve$"` when done.
  Keychain is shared, packs are per-root.

## Conventions

- TDD: write the failing test first, then the fix; name the test in the commit
  (`internal/core/starting_test.go` is the pattern; app tests in `app/Tests`).
- Commit subjects: imperative, area-prefixed where it helps (`App: ...`,
  `MCP: ...`, `Guest: ...`, `docs: ...`, `make guest-swap: ...`). Issue titles
  are conventional-commit style (`feat: ...`, `bug: ...`).
- Architecture decisions go in `docs/decisions.md` as `D<n>` (edit an entry
  to revise it; don't add a duplicate).
- Lint: golangci v2 (`.golangci.yml`: errcheck, gosec, errorlint, bodyclose,
  noctx, unparam, ...) plus standalone gosec + govulncheck. G110/G301 are
  excluded by config; Close errors on io.Closer/os.File/net.Conn are exempt.
- Core rules: every running-VM lookup goes through `instance()` (refuses a
  starting VM); never hold `c.mu` across code that can panic; release locks
  with `defer`. Secret values never cross MCP (key names only).

## Roadmap

Open GitHub issues (`gh issue list`) plus the prioritized list in the
auto-memory file `onyx-next-steps.md`. Operational gotchas live in
`onyx-project-state.md` and `onyx-dogfood-loop.md` there.

## Finishing an issue

1. `make ci` green (and `make image` + re-import if the guest changed).
2. Decision recorded in `docs/decisions.md` if the change fixes a design choice.
3. Close the issue with a comment naming the commit (`gh issue close N -c "..."`).
4. Update memory `onyx-next-steps.md` (drop the item; add anything new learned
   to project-state/dogfood-loop only if it is a non-obvious gotcha).
5. Commit and push, then `/clear`. A fresh session reads this file →
   next-steps → the issue.
