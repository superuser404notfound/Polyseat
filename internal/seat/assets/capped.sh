#!/bin/sh
# Polyseat - start a game from the desktop behind the framerate cap.
#
#   polyseat-capped COMMAND [ARG...]
#
# Every game Polyseat puts in the seat's own launcher starts through this. It
# sets the two variables that carry the cap and replaces itself with the game,
# so what runs is the game and nothing stays behind waiting for it.
#
# **This used to be an env line in the entry itself, and that stopped working
# the moment the launcher changed.**
#
#     Exec=env MANGOHUD=1 LD_PRELOAD=/usr/$LIB/mangohud/libMangoHud_shim.so ...
#
# $LIB belongs to the dynamic linker, which picks the 32 or the 64 bit build of
# the shim with it, so it has to reach the game unexpanded. fuzzel ran an Exec
# line without a shell and it did. nwg-drawer runs it through `env -S`, and GNU
# env refuses a bare $NAME there outright:
#
#     env: only ${VARNAME} expansion is supported, error at: $LIB/...
#
# exit 125, nothing on screen, and every game in the grid dead while Steam and
# Firefox beside them start fine. Measured with the entry exactly as the daemon
# wrote it. ${LIB} would have been expanded by env instead, to nothing.
#
# So no launcher gets to see the dollar sign at all. The entry names this file
# and the words of the game's own command, which Polyseat keeps to characters
# that need no quoting anywhere, and the value is set here where a shell reads
# it the way it is written.
#
# The desktop itself still has neither variable's preload, on purpose:
# polyseat-launcher says why.

if [ "$#" -eq 0 ]; then
    echo "polyseat-capped: nothing to start" >&2
    exit 2
fi

export MANGOHUD=1

# Single quotes, because the linker expands this and a shell must not.
# shellcheck disable=SC2016
LD_PRELOAD='/usr/$LIB/mangohud/libMangoHud_shim.so'
export LD_PRELOAD

exec "$@"
