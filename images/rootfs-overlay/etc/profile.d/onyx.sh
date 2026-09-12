# Secrets delivered by the Onyx host live in tmpfs; pick them up in login shells.
[ -r /run/onyx/env ] && . /run/onyx/env
