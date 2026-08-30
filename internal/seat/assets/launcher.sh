#!/bin/sh
# Polyseat - show, hide or toggle the application launcher in a seat.
#
#   polyseat-launcher show     open it if it is not already open
#   polyseat-launcher hide     close it
#   polyseat-launcher toggle   the other one
#   polyseat-launcher refresh  re-read the menu, if it is open
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
#
# Newest first, because a socket left behind by a previous sway is still lying
# there after a restart and picking it is indistinguishable from picking the
# live one until something tries to draw. -type s is what keeps the .lock file
# beside each socket out, so nothing has to be filtered by name.
if [ -z "$WAYLAND_DISPLAY" ]; then
    sock=$(find "$XDG_RUNTIME_DIR" -maxdepth 1 -type s -name 'wayland-[0-9]*' \
        -printf '%T@ %p\n' 2>/dev/null | sort -rn | head -1 | cut -d' ' -f2-)
    [ -n "$sock" ] && WAYLAND_DISPLAY="${sock##*/}" && export WAYLAND_DISPLAY
fi

# Where the desktop entries of the player's own flatpaks are, for the same
# reason and with a worse symptom.
#
# polyseat-sway.service sets this for the session, and not every caller comes
# from the session: a prep command inherits Sunshine's unit, which does not set
# it, and a refresh from the daemon arrives through `incus exec` with no session
# environment at all. In both, fuzzel falls back to the two system directories
# and every flatpak the player installed is missing from the menu, while
# `flatpak run` starts the same application from a terminal without complaint.
# Nothing about that says the launcher is the reason.
#
# Prepended one at a time rather than assigned, so that the session's own value
# is kept where there is one and this stays the same list either way.
for d in "$HOME/.local/share/flatpak/exports/share" /var/lib/flatpak/exports/share; do
    case ":$XDG_DATA_DIRS:" in
        *":$d:"*) ;;
        *) XDG_DATA_DIRS="$d${XDG_DATA_DIRS:+:$XDG_DATA_DIRS}" ;;
    esac
done

# The default the specification gives when the variable is unset, which setting
# it at all is exactly how you lose. Without this a launcher opened from a prep
# command would list the flatpaks and nothing else.
case ":$XDG_DATA_DIRS:" in
    *":/usr/share:"*) ;;
    *) XDG_DATA_DIRS="$XDG_DATA_DIRS:/usr/local/share:/usr/share" ;;
esac

export XDG_DATA_DIRS

running() { pgrep -x fuzzel >/dev/null 2>&1; }

show() {
    running && return 0

    # The desktop does not inherit the OpenGL half of the framerate cap.
    #
    # Sunshine applies its app environment to the prep command that opens this,
    # so without this line everything somebody starts from the launcher, and
    # everything started from a terminal opened from it, runs with MangoHud's
    # shim preloaded. Firefox dies of that immediately, every time: measured in
    # a seat, SIGSEGV during EGL setup with a minidump and nothing on screen.
    #
    # Only the preload goes. MANGOHUD stays set, so a Vulkan game started from
    # the launcher is still capped, and the games Polyseat puts in this menu
    # carry the preload in their own entries, so they are capped as well. What
    # loses the cap is a game started by hand that is neither: OpenGL, and not
    # one of ours.
    unset LD_PRELOAD

    # Detached, because a prep command that does not return holds up the
    # stream it is preparing.
    setsid fuzzel >/dev/null 2>&1 </dev/null &
}

hide() {
    pkill -x fuzzel >/dev/null 2>&1

    # pkill returns before the process is gone, and show does nothing while one
    # is still running. Without the wait a refresh is a race that closes the
    # launcher and leaves nothing in its place.
    i=0
    while running && [ "$i" -lt 50 ]; do
        sleep 0.1
        i=$((i + 1))
    done

    return 0
}

# Re-read the menu after something was installed or removed.
#
# fuzzel builds its list once, when it starts. The launcher opened when the
# session came up is left sitting on the overlay layer for as long as nobody
# dismisses it, so a flatpak installed afterwards is missing from a menu that is
# already on screen, and show does nothing because one is running. The daemon
# calls this after an install for the same reason it rewrites Moonlight's list.
#
# Only when one is open. Putting a launcher over somebody's game because a
# download finished would be worse than the menu being one entry out of date.
refresh() {
    running || return 0

    hide
    show
}

case "$1" in
    hide) hide ;;
    refresh) refresh ;;
    toggle)
        if running; then hide; else show; fi
        ;;
    *) show ;;
esac

exit 0
