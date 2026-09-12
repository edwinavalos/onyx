# Onyx work user login profile.
export PATH="$HOME/.local/bin:$PATH"
[ -r /run/onyx/env ] && . /run/onyx/env

# On the serial console, hand off to the session the host asked for.
# /run/onyx/session is written by onyx-guest and defines
#   ONYX_SESSION_DIR, ONYX_SESSION_CMD, ONYX_ROWS, ONYX_COLS
if [ "$(tty)" = "/dev/hvc0" ]; then
    i=0
    while [ ! -r /run/onyx/session ] && [ $i -lt 100 ]; do
        [ $i -eq 0 ] && printf 'onyx: waiting for session...\n'
        sleep 0.2; i=$((i+1))
    done
    if [ -r /run/onyx/session ]; then
        . /run/onyx/session
        [ -n "$ONYX_ROWS" ] && stty rows "$ONYX_ROWS" cols "$ONYX_COLS" 2>/dev/null
        cd "${ONYX_SESSION_DIR:-$HOME}" || cd "$HOME"
        if [ -n "$ONYX_SESSION_CMD" ]; then
            printf 'onyx: %s\n' "$ONYX_SESSION_CMD"
            eval "$ONYX_SESSION_CMD"
            printf '\nonyx: session command exited (%s); dropping to shell\n' "$?"
        fi
    fi
fi
