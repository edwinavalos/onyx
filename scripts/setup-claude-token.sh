#!/usr/bin/env bash
# Set up Claude Code auth for Onyx VMs the durable way: a long-lived token
# from `claude setup-token` stored in the macOS Keychain (Onyx service),
# exposed to VMs only through the credential proxy.
#
# Interactive: `claude setup-token` opens a browser for login and prints the
# token; you paste it into the hidden prompt. Nothing here echoes the token.
#
#   scripts/setup-claude-token.sh [--test]
#
# --test boots a throwaway VM afterwards and runs `claude -p` through the proxy.
set -euo pipefail

ONYX="${ONYX:-$(cd "$(dirname "$0")/.." && pwd)/bin/onyx}"
KEY="${KEY:-claude-token}"
PACK="${PACK:-claude}"
TEST=0
[ "${1:-}" = "--test" ] && TEST=1

say() { printf '\n==> %s\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

[ -x "$ONYX" ] || die "onyx binary not found at $ONYX (run: make sign)"
command -v claude >/dev/null || die "claude CLI not found on the host"

# Onyx's CLI talks to the core; start a temporary one if none is running.
STARTED_SERVE=0
if ! "$ONYX" vm ls >/dev/null 2>&1; then
    say "starting onyx serve (temporary, for this script)"
    "$ONYX" serve >/tmp/onyx-setup-serve.log 2>&1 &
    SERVE_PID=$!
    STARTED_SERVE=1
    for _ in $(seq 1 20); do "$ONYX" vm ls >/dev/null 2>&1 && break; sleep 0.25; done
    "$ONYX" vm ls >/dev/null 2>&1 || die "onyx serve did not come up (see /tmp/onyx-setup-serve.log)"
fi
cleanup() { [ "$STARTED_SERVE" = 1 ] && kill -INT "$SERVE_PID" 2>/dev/null; true; }
trap cleanup EXIT

say "1/3  Get a long-lived token from Claude Code"
cat <<MSG
This runs \`claude setup-token\`: it opens your browser to authorize and then
prints a token (starts with sk-ant-oat...). Copy it; you'll paste it next.
MSG
read -r -p "Press Enter to run claude setup-token..." _
claude setup-token

say "2/3  Store it in the Keychain (hidden prompt; not echoed, not in argv)"
if "$ONYX" secret ls | grep -q "^${KEY}\b"; then
    echo "replacing existing secret '${KEY}'"
fi
"$ONYX" secret set "$KEY"

say "3/3  Pack '${PACK}': proxy mode, so VMs never hold the token"
"$ONYX" pack rm "$PACK" >/dev/null 2>&1 || true
"$ONYX" pack create "$PACK" -secret "${KEY}>https://api.anthropic.com>bearer"
"$ONYX" pack show "$PACK"

say "done"
echo "Use it with:  onyx run -pack ${PACK}"

if [ "$TEST" = 1 ]; then
    say "test: booting a VM and running claude -p through the proxy"
    "$ONYX" image ls | grep -q . || die "no image installed (make image && onyx image import base images/out)"
    NAME="claude-token-test-$$"
    "$ONYX" volume create "${NAME}-state" -size 512 >/dev/null
    "$ONYX" vm create "$NAME" -pack "$PACK" -volume "${NAME}-state:/home/dev/.claude" >/dev/null
    "$ONYX" vm start "$NAME" >/dev/null
    set +e
    "$ONYX" vm exec "$NAME" -- su dev -c 'bash -lc "cd ~ && claude -p \"Reply with the single word OK.\" 2>&1 | tail -1"'
    RC=$?
    set -e
    "$ONYX" vm stop "$NAME" >/dev/null 2>&1 || true
    "$ONYX" vm rm "$NAME" >/dev/null 2>&1 || true
    "$ONYX" volume rm "${NAME}-state" >/dev/null 2>&1 || true
    [ "$RC" = 0 ] && echo "test passed" || die "test failed (rc=$RC)"
fi
