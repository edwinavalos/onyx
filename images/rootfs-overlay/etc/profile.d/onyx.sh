# Secrets delivered by the Onyx host live in tmpfs; pick them up in login shells.
[ -r /run/onyx/env ] && . /run/onyx/env

# Go's GC keeps the compiler and linker under a soft ceiling of ~60% of RAM
# (a hint, not a cap: it only trades CPU for memory when a build gets
# close). Fits a default 512 MB guest with Claude Code alongside.
if [ -z "$GOMEMLIMIT" ]; then
    export GOMEMLIMIT="$(awk '/MemTotal/ {printf "%dMiB", $2 * 6 / 10 / 1024}' /proc/meminfo)"
fi
