# Onyx work user login profile.
export PATH="$HOME/.local/bin:$PATH"

# Secrets and proxies delivered by the host live in tmpfs. They may arrive
# after this shell starts (the console logs in at boot), so this is a
# function we call again once the session request shows up.
onyx_load_env() {
    [ -r /run/onyx/env ] && . /run/onyx/env
    # If the host proxies api.anthropic.com, route Claude Code through it
    # with a placeholder credential; the proxy swaps in the real one.
    # Anything delivered explicitly (env mode) wins.
    if [ -n "$ONYX_PROXY_API_ANTHROPIC_COM" ]; then
        export ANTHROPIC_BASE_URL="$ONYX_PROXY_API_ANTHROPIC_COM"
        if [ -z "$ANTHROPIC_API_KEY" ] && [ -z "$CLAUDE_CODE_OAUTH_TOKEN" ]; then
            case "$ONYX_PROXY_API_ANTHROPIC_COM_AUTH" in
                bearer) export CLAUDE_CODE_OAUTH_TOKEN=onyx-proxied ;;
                *)      export ANTHROPIC_API_KEY=onyx-proxied ;;
            esac
        fi
    fi
    # gh speaks TLS to api.github.com; when the host proxies that origin it
    # terminates TLS (the Onyx CA is in the system bundle) and swaps this
    # placeholder for the real token. An explicit GH_TOKEN (env mode) wins.
    if [ -n "$ONYX_PROXY_API_GITHUB_COM" ] && [ -z "$GH_TOKEN" ]; then
        export GH_TOKEN=onyx-proxied
    fi
}
onyx_load_env

# On the serial console, hand off to the session the host asked for.
# /run/onyx/session is written by onyx-guest and defines
#   ONYX_SESSION_DIR, ONYX_SESSION_CMD, ONYX_SESSION_EXIT, ONYX_ROWS, ONYX_COLS
# The host always sends one at the end of a start (a plain shell when the
# user asked for nothing), so this normally returns within a second of
# login; the cap only guards against a host that died mid-start.
if [ "$(tty)" = "/dev/hvc0" ]; then
    i=0
    while [ ! -r /run/onyx/session ] && [ $i -lt 600 ]; do
        [ $i -eq 0 ] && printf 'onyx: waiting for the host to finish setup...\n'
        sleep 0.2; i=$((i+1))
    done
    if [ -r /run/onyx/session ]; then
        onyx_load_env
        . /run/onyx/session
        [ -n "$ONYX_ROWS" ] && stty rows "$ONYX_ROWS" cols "$ONYX_COLS" 2>/dev/null
        cd "${ONYX_SESSION_DIR:-$HOME}" 2>/dev/null || cd "$HOME"
        if [ -n "$ONYX_SESSION_CMD" ]; then
            printf 'onyx: %s\n' "$ONYX_SESSION_CMD"
            eval "$ONYX_SESSION_CMD"
            rc=$?
            if [ "$ONYX_SESSION_EXIT" = "poweroff" ]; then
                printf '\nonyx: session command exited (%s); powering off\n' "$rc"
                sudo poweroff
                exit 0
            fi
            printf '\nonyx: session command exited (%s); dropping to shell\n' "$rc"
        fi
    fi
fi
