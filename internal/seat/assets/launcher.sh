#!/bin/sh
# Polyseat - show, hide or toggle the application launcher in a seat.
#
#   polyseat-launcher show     open it if it is not already open
#   polyseat-launcher hide     close it
#   polyseat-launcher toggle   the other one
#
# The launcher is opened for you when the session comes up, so that picking
# "Desktop" in Moonlight lands on something you can choose from rather than on
# a terminal. That is the whole reason this is not simply `exec fuzzel`: opening
# it has to be repeatable, from a key, from the bar, and from Sunshine's prep
# command when a client connects, without ever ending up with two of them.
#
# Sunshine hides it again when anything else is picked. Without that, choosing
# Steam Big Picture from a client would start Big Picture underneath a launcher
# that is still sitting on the overlay layer waiting to be dismissed.

: "${XDG_RUNTIME_DIR:=/run/user/$(id -u)}"
export XDG_RUNTIME_DIR

# Sunshine's prep commands and sway's exec do not agree about what is in the
# environment, and fuzzel needs a display either way.
if [ -z "$WAYLAND_DISPLAY" ]; then
    sock=$(ls -t "$XDG_RUNTIME_DIR"/wayland-[0-9]* 2>/dev/null | grep -v '\.lock$' | head -1)
    [ -n "$sock" ] && WAYLAND_DISPLAY="${sock##*/}" && export WAYLAND_DISPLAY
fi

running() { pgrep -x fuzzel >/dev/null 2>&1; }

show() {
    running && return 0

    # Detached, because a prep command that does not return holds up the
    # stream it is preparing.
    setsid fuzzel >/dev/null 2>&1 </dev/null &
}

hide() {
    pkill -x fuzzel >/dev/null 2>&1
    return 0
}

case "$1" in
    hide) hide ;;
    toggle)
        if running; then hide; else show; fi
        ;;
    *) show ;;
esac

exit 0
