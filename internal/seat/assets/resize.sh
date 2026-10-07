#!/bin/sh
# Polyseat - match the seat's output to the client that is connecting.
#
#   polyseat-resize                    take the size from Sunshine's environment
#   polyseat-resize 1920x1080@60Hz     set an explicit mode
#
# Sunshine exports SUNSHINE_CLIENT_WIDTH, SUNSHINE_CLIENT_HEIGHT and
# SUNSHINE_CLIENT_FPS to the commands in global_prep_cmd, so Sunshine runs this
# with no arguments. The daemon passes the seat's configured mode back in once
# a stream has stayed gone; Sunshine has no undo for it, see sunshine.conf.
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
#
# The width is handed on exactly as the client asked for it, and one thing is
# worth writing down about that. A seat whose width is not a multiple of four
# cannot be photographed: it reads a frame back in a 24-bit format, grim rounds
# the stride up to four bytes, wlroots wants a stride that is a whole number of
# three-byte pixels, and the two only agree on a multiple of four.
#
#     2248 wide   stride 6744   2248 pixels exactly        a screenshot
#     2250 wide   stride 6752   2250 pixels and two bytes  "Invalid stride"
#
# A phone asking for 2250x1206 leaves the seat like that, and everything that
# takes a screenshot there fails, xdg-desktop-portal-wlr included, since it
# shells out to grim. Nothing in a seat needs a screenshot today - the one
# thing that did, in 0.22.0, was taken out again - so the client gets the width
# it asked for. If something here ever has to read the screen, this is the
# first thing to check, and rounding the width down by up to three pixels is
# the whole fix.

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

    # Twice the client's rate, and exactly twice.
    #
    # The output has no display behind it, so its refresh rate is not a clock
    # that anything is held to. It is the shortest gap sway leaves between two
    # frames, and a game does not deliver its frames evenly: measured in a seat
    # on 2026-10-07, one capped at 60 came in after 17 ms, after 18.5, and then
    # one after 5 to catch up. At 60 Hz there is one place per 16.7 ms and the
    # catch-up frame has nowhere to go, so the stream loses it: 54 to 57 frames
    # a second reached a 60 fps client, with four or five gaps of 33 ms in
    # every one of them. At 120 Hz every frame has a place, and the same seat
    # delivered 60.00 with none.
    #
    # Exactly twice, because gamescope takes the output's rate as its own and
    # can only hold a game to a whole fraction of it. 90 Hz for a 60 fps client
    # gave 43 frames a second and 240 Hz gave 57.
    #
    # Sunshine is what keeps the stream at the client's rate. It captures a
    # frame when sway has one, see packaging/sunshine, and holds that to the
    # rate the client asked for; and it only captures that way on an output
    # that is faster than the stream, so this and that belong together.
    mode="${width}x${height}@$((fps * 2))Hz"
fi

if swaymsg -- output HEADLESS-1 mode "$mode" >/dev/null 2>&1; then
    say "output set to $mode"
else
    say "sway refused the mode $mode, leaving the resolution alone"
fi

exit 0
