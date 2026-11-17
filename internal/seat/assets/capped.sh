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

#   polyseat-capped -C DIR COMMAND [ARG...]
#
# starts it in DIR, which is written with every byte outside [A-Za-z0-9/._,:+-]
# as @ and three octal digits, a space as @040.
#
# **The folder cannot simply be quoted, and v0.50.1 did that.** nwg-drawer
# removes every " and ' from an Exec line when it reads the entry, before env -S
# ever sees it, so
#
#     Exec=... env -C "/home/player/games/shared/DinoBones Tidy Up the Museum" ...
#
# reached env as -C /home/player/games/shared/DinoBones and four more words:
# exit 125, nothing on screen, for every game whose folder has a space in it.
# The same game started fine from Faugus's own window, which sets the folder
# itself. Nothing in the line is left for either reader to take apart: no
# quote, no backslash, no dollar, no %, which the drawer cuts at, and no space.

if [ "$1" = "-C" ] && [ "$#" -ge 2 ]; then
    # Back to \ooo, which printf reads in its format on every shell. Nothing
    # else in the encoded form is special to printf, since \ and % are encoded
    # themselves. The x keeps a trailing newline from being eaten by $().
    # shellcheck disable=SC2059
    dir=$(printf "$(printf '%s' "$2" | sed 's/@\([0-7][0-7][0-7]\)/\\\1/g')x")
    dir=${dir%x}

    if ! cd -- "$dir"; then
        echo "polyseat-capped: cannot start in $dir" >&2
        exit 125
    fi

    shift 2
fi

if [ "$#" -eq 0 ]; then
    echo "polyseat-capped: nothing to start" >&2
    exit 2
fi

export MANGOHUD=1

# Single quotes, because the linker expands this and a shell must not.
#
# Polyseat's build of MangoHud's shim and not the distribution's: that one and
# Steam's overlay together hang every game Steam starts. mangoHudPreload in
# apps.go says why.
# shellcheck disable=SC2016
LD_PRELOAD='/usr/$LIB/polyseat/mangohud/libMangoHud_shim.so'
export LD_PRELOAD

exec "$@"
