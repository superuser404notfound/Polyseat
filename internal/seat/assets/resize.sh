#!/bin/sh
# Polyseat - match the seat's output to the client that is connecting.
#
#   polyseat-resize                    take the size from Sunshine's environment
#   polyseat-resize 1920x1080@60Hz     set an explicit mode
#
# Sunshine exports SUNSHINE_CLIENT_WIDTH, SUNSHINE_CLIENT_HEIGHT and
# SUNSHINE_CLIENT_FPS to the commands in global_prep_cmd, so the "do" side
# needs no arguments and the "undo" side passes the seat's configured mode back
# in.
#
# Without this the output stays at whatever the session came up with and every
# client gets that, scaled. A phone streaming 1280x720 from a 1920x1080 seat
# pays for pixels it cannot show, and a client with a larger or differently
# shaped screen gets black bars that no setting on its end can remove.
#
# Failure here must never take the stream down with it. A seat at the wrong
# resolution still plays; a seat whose prep command returned non-zero does not
# start at all. Every path below therefore ends in exit 0, and says on the way
# out what it did.

: "${XDG_RUNTIME_DIR:=/run/user/$(id -u)}"
export XDG_RUNTIME_DIR

say() { echo "polyseat-resize: $*" >&2; }

# Sunshine inherits its environment from the user manager, which has no
# SWAYSOCK. Found rather than assumed, and the newest one wins so that a
# restarted session does not leave this pointing at a dead socket.
if [ -z "$SWAYSOCK" ] || [ ! -S "$SWAYSOCK" ]; then
    SWAYSOCK=$(ls -t "$XDG_RUNTIME_DIR"/sway-ipc.* 2>/dev/null | head -1)
    export SWAYSOCK
fi

if [ -z "$SWAYSOCK" ] || [ ! -S "$SWAYSOCK" ]; then
    say "no sway socket in $XDG_RUNTIME_DIR, leaving the resolution alone"
    exit 0
fi

mode=$1

if [ -z "$mode" ]; then
    width=$SUNSHINE_CLIENT_WIDTH
    height=$SUNSHINE_CLIENT_HEIGHT
    fps=$SUNSHINE_CLIENT_FPS

    # Sunshine sets these for every stream, so an empty value means the version
    # in this seat does not, and guessing a size would be worse than not
    # touching it.
    if [ -z "$width" ] || [ -z "$height" ]; then
        say "Sunshine reported no client size, leaving the resolution alone"
        exit 0
    fi

    case "$width$height" in
        *[!0-9]* | '') say "client size '${width}x${height}' is not a size"; exit 0 ;;
    esac

    # A headless output still needs a refresh rate, and Sunshine has been seen
    # to leave the framerate out.
    case "$fps" in
        '' | *[!0-9]*) fps=60 ;;
        0) fps=60 ;;
    esac

    mode="${width}x${height}@${fps}Hz"
fi

if swaymsg -- output HEADLESS-1 mode "$mode" >/dev/null 2>&1; then
    say "output set to $mode"
else
    say "sway refused the mode $mode, leaving the resolution alone"
fi

exit 0
