#!/bin/sh
# Polyseat - put the seat's screen on one of its two workspaces.
#
#   polyseat-workspace 1     the desktop: terminal, launcher grid, everything
#                            somebody installs
#   polyseat-workspace 2     gamescope, and therefore Steam
#
# A script rather than a swaymsg in the application entry, and that is the same
# lesson polyseat-capped carries: Sunshine takes the command apart itself and
# does not give a shell the quotes to work with. A `sh -c '...'` written into
# the entry arrives as words, the socket expansion never happens, and the switch
# silently does nothing - which is exactly what it did.
#
# The socket is looked up here for the same reason. Sunshine's own environment
# has no SWAYSOCK, because it is a user unit that started before the session
# opened one.

: "${XDG_RUNTIME_DIR:=/run/user/$(id -u)}"
export XDG_RUNTIME_DIR

say() { echo "polyseat-workspace: $*" >&2; }

case "$1" in
    1 | 2) ;;
    *)
        say "usage: polyseat-workspace 1|2"

        exit 2
        ;;
esac

if [ -z "$SWAYSOCK" ] || [ ! -S "$SWAYSOCK" ]; then
    SWAYSOCK=$(ls -t "$XDG_RUNTIME_DIR"/sway-ipc.* 2>/dev/null | head -1)
    export SWAYSOCK
fi

if [ ! -S "$SWAYSOCK" ]; then
    say "no session to talk to, the screen was left where it was"

    exit 0
fi

# Never fails. This runs as a Sunshine prep command, and a prep command that
# returns non-zero stops the stream it was meant to prepare.
swaymsg workspace number "$1" >/dev/null 2>&1 ||
    say "the switch to workspace $1 did not take"

exit 0
