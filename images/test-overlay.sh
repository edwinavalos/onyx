#!/usr/bin/env bash
# Host-side tests for the guest overlay scripts (they only need node and sh).
#   images/test-overlay.sh
set -euo pipefail
OV="$(cd "$(dirname "$0")/rootfs-overlay" && pwd)/usr/local/bin"
# pwd -P: process.cwd() resolves /var → /private/var on macOS.
T="$(mktemp -d)"; T="$(cd "$T" && pwd -P)"; trap 'rm -rf "$T"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }
json() { node -e 'const c=JSON.parse(require("fs").readFileSync(process.argv[1],"utf8"));console.log(eval("c."+process.argv[2]))' "$1" "$2"; }

# 1. onyx-trust with no ~/.claude at all (New VM without a state volume):
#    ~/.claude.json is a symlink into ~/.claude like the image ships it, and
#    that directory does not exist yet.
export HOME="$T/home1"; mkdir -p "$HOME/work"; ln -s .claude/claude.json "$HOME/.claude.json"
(cd "$HOME/work" && "$OV/onyx-trust")
[ "$(json "$HOME/.claude.json" hasCompletedOnboarding)" = true ] || fail "onboarding not pre-accepted without ~/.claude"
[ "$(json "$HOME/.claude.json" theme)" = dark ] || fail "no default theme"
[ "$(json "$HOME/.claude.json" "projects['$HOME/work'].hasTrustDialogAccepted")" = true ] || fail "cwd not trusted"

# 2. Existing state: the user's theme and other keys survive.
export HOME="$T/home2"; mkdir -p "$HOME/.claude" "$HOME/p"; ln -s .claude/claude.json "$HOME/.claude.json"
echo '{"theme":"light","userID":"u1","projects":{"/x":{"hasTrustDialogAccepted":true}}}' > "$HOME/.claude/claude.json"
(cd "$HOME/p" && "$OV/onyx-trust")
[ "$(json "$HOME/.claude.json" theme)" = light ] || fail "theme overwritten"
[ "$(json "$HOME/.claude.json" userID)" = u1 ] || fail "userID lost"
[ "$(json "$HOME/.claude.json" "projects['/x'].hasTrustDialogAccepted")" = true ] || fail "other project trust lost"
[ "$(json "$HOME/.claude.json" "projects['$HOME/p'].hasTrustDialogAccepted")" = true ] || fail "cwd not trusted"

# 3. The `claude` wrapper pre-accepts and then execs the real CLI with args.
export HOME="$T/home3"; mkdir -p "$HOME/w"; ln -s .claude/claude.json "$HOME/.claude.json"
printf '#!/bin/sh\necho "cli:$*"\n' > "$T/claude-cli"; chmod +x "$T/claude-cli"
out="$(cd "$HOME/w" && ONYX_CLAUDE_CLI="$T/claude-cli" "$OV/claude" -p hello)"
[ "$out" = "cli:-p hello" ] || fail "wrapper did not exec the CLI: $out"
[ "$(json "$HOME/.claude.json" hasCompletedOnboarding)" = true ] || fail "wrapper skipped onyx-trust"

# 4. The Codex wrapper always redirects its complete state, including auth,
# to tmpfs through CODEX_HOME.
printf '#!/bin/sh\nprintf "home:%%s args:%%s\\n" "$CODEX_HOME" "$*"\n' > "$T/codex-cli"; chmod +x "$T/codex-cli"
mkdir -p "$T/codex-bin"; cp "$OV/codex" "$T/codex-bin/codex"; chmod +x "$T/codex-bin/codex"; cp "$T/codex-cli" "$T/codex-bin/codex-cli"
out="$(HOME="$T/home4" ONYX_CODEX_HOME="$T/codex-home" "$T/codex-bin/codex" login --device-auth)"
[ "$out" = "home:$T/codex-home args:login --device-auth" ] || fail "Codex wrapper did not set CODEX_HOME: $out"

# 5. Permissions: the VM is the sandbox, so Claude Code runs in bypass mode
#    without a flag — the one-time acknowledgement is pre-accepted and
#    settings.json on the state volume gets the default mode.
export HOME="$T/home5"; mkdir -p "$HOME/w"; ln -s .claude/claude.json "$HOME/.claude.json"
(cd "$HOME/w" && "$OV/onyx-trust")
[ "$(json "$HOME/.claude.json" bypassPermissionsModeAccepted)" = true ] || fail "bypass mode not pre-acknowledged"
[ "$(json "$HOME/.claude/settings.json" permissions.defaultMode)" = bypassPermissions ] || fail "settings.json lacks defaultMode"
[ "$(json "$HOME/.claude/settings.json" skipDangerousModePermissionPrompt)" = true ] || fail "interactive bypass dialog not skipped"

# 6. A mode the user chose on the volume, and their other settings, survive.
export HOME="$T/home6"; mkdir -p "$HOME/.claude" "$HOME/w"; ln -s .claude/claude.json "$HOME/.claude.json"
echo '{"permissions":{"defaultMode":"acceptEdits","allow":["Bash(git:*)"]},"model":"opus"}' > "$HOME/.claude/settings.json"
(cd "$HOME/w" && "$OV/onyx-trust")
[ "$(json "$HOME/.claude/settings.json" permissions.defaultMode)" = acceptEdits ] || fail "user's defaultMode overwritten"
[ "$(json "$HOME/.claude/settings.json" "permissions.allow[0]")" = "Bash(git:*)" ] || fail "allow list lost"
[ "$(json "$HOME/.claude/settings.json" model)" = opus ] || fail "model lost"

echo "overlay tests passed"
