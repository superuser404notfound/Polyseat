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
# a terminal. That is the whole reason this is not simply `exec nwg-drawer`:
# opening it has to be repeatable, from a key, from the bar, and from Sunshine's
# prep command when a client connects, without ever ending up with two of them.
#
# Sunshine hides it again when anything else is picked. Without that, choosing
# Steam Big Picture from a client would start Big Picture underneath a launcher
# that is still sitting on the overlay layer waiting to be dismissed.
#
# The launcher is nwg-drawer, a grid of large icons over the whole screen. It
# replaced fuzzel, which drew a list of eighteen narrow rows: fine with a mouse,
# and a row of text a few millimetres tall is a poor thing to aim a thumbstick
# at on a phone. The grid is a target the size of an icon, and it can be used
# without aiming at all: the D-pad moves between icons, Y or Start starts one,
# B closes it.

: "${XDG_RUNTIME_DIR:=/run/user/$(id -u)}"
export XDG_RUNTIME_DIR

# Sunshine's prep commands and sway's exec do not agree about what is in the
# environment, and the launcher needs a display either way.
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

# The same for sway's own socket, which is how the grid learns how large the
# screen is.
#
# Missing in every caller that matters: the session imports WAYLAND_DISPLAY,
# XDG_SESSION_TYPE and DISPLAY into the user manager and not this, so a prep
# command has none, and neither has a call arriving through `incus exec`.
# Measured rather than reasoned about: without this, asking sway anything from
# here answers "Unable to retrieve socket path", and the launcher opened at the
# size meant for a 1080p screen on a 4K one. polyseat-resize finds it the same
# way, newest first for the same reason as above.
if [ -z "$SWAYSOCK" ] || [ ! -S "$SWAYSOCK" ]; then
    SWAYSOCK=$(find "$XDG_RUNTIME_DIR" -maxdepth 1 -type s -name 'sway-ipc.*' \
        -printf '%T@ %p\n' 2>/dev/null | sort -rn | head -1 | cut -d' ' -f2-)
    export SWAYSOCK
fi

# Where the desktop entries of the player's own flatpaks are, for the same
# reason and with a worse symptom.
#
# polyseat-sway.service sets this for the session, and not every caller comes
# from the session: a prep command inherits Sunshine's unit, which does not set
# it, and a refresh from the daemon arrives through `incus exec` with no session
# environment at all. In both, fuzzel fell back to the two system directories
# and every flatpak the player installed was missing from the menu, while
# `flatpak run` started the same application from a terminal without complaint.
# Nothing about that said the launcher was the reason.
#
# nwg-drawer looks in the two flatpak directories for entries by itself, so
# for it the list survives. What does not is everything else read along this
# variable: GTK finds a flatpak's icon there, and the drawer finds its own
# category definitions under /usr/share, which is lost the moment the variable
# is set without it. Read in its source, not yet seen in a seat.
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

running() { pgrep -x nwg-drawer >/dev/null 2>&1; }

# How tall the screen is, or nothing when it cannot be asked.
#
# The output is whatever the connected client asked for, so this is a different
# number on a phone and on a television, and it decides how large the grid is
# drawn. Read through python rather than parsed out of the JSON by hand: python
# is in every seat because the input helpers need it, and a launcher that got
# this wrong by a regular expression would open at the wrong size with nothing
# saying why.
screen_height() {
    swaymsg -t get_outputs 2>/dev/null | python3 -c '
import json, sys

try:
    outputs = json.load(sys.stdin)
except Exception:
    raise SystemExit(0)

# The first output with a size. One sway knows about but is not driving reports
# zero, the same as polyseat-pad-pointer has to allow for.
for output in outputs if isinstance(outputs, list) else []:
    height = (output.get("rect") or {}).get("height") or 0
    if height:
        print(height)
        break
' 2>/dev/null
}

# Whether to draw the grid at double size, as 1 or 2.
#
# The seat's output is whatever the client asked for, and 96 pixel icons that
# fill a phone are a strip of stamps on a 4K television across a room. So the
# grid follows the screen.
#
# **GDK_SCALE does not do this, which is what the first version tried.** It is
# set, it is in the process environment, and GTK on Wayland ignores it: measured
# in a seat at 3840x2160, where the drawer came up drawn exactly as it is at
# 1080p. What does work is asking for larger icons outright and scaling the text
# with GDK_DPI_SCALE, which is two settings rather than one and is why they are
# multiplied out below.
#
# One threshold rather than a curve, because the icon sizes worth having are
# doubles of each other. 1800 puts 4K on one side and 1440p on the other: at
# 2160 the unscaled grid is unusable across a room, and at 1440 a doubled one
# would leave two rows on the screen.
#
# Anything unreadable answers 1, which is what was measured at 1080p.
drawer_scale() {
    height=$(screen_height)

    case "$height" in
        '' | *[!0-9]*) echo 1; return ;;
    esac

    if [ "$height" -ge 1800 ]; then echo 2; else echo 1; fi
}

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
    #
    # Not resident (-r). A resident drawer is a process that is always there
    # and only sometimes visible, and every verb below is written against
    # "running means on screen". Started fresh each time it also reads the
    # entries each time, which is what refresh relies on.
    #
    # -ovl puts it over a fullscreen window as fuzzel was, so picking Desktop
    # in Moonlight over a Big Picture that is still running lands on the grid
    # rather than behind it. -mt keeps the bar's height clear, which is the
    # reason for the margin rather than tidiness: over the bar it would cover
    # the Apps button that closes it and the Keyboard button, and a pointer
    # has no other way to either.
    #
    # -nofs, because file search is the drawer typing a path into a results
    # pane, and a seat is driven from a controller more often than not.
    # -closebtn gives the pointer something visible to press, since Escape is
    # B and nothing on screen says so.
    #
    # And no -wm. With it the drawer starts applications through `swaymsg
    # exec`, which is `sh -c`, and that is a second round of interpretation for
    # every Exec line with nothing gained: the drawer already detaches what it
    # starts, and what it starts should inherit the environment set up above.
    scale=$(drawer_scale)

    # The text, which no flag covers.
    [ "$scale" -gt 1 ] && export GDK_DPI_SCALE="$scale"

    # The margin is not multiplied. It keeps the bar clear, and the bar is 30
    # pixels tall whatever the client asked for: waybar is not scaled with this
    # and a doubled margin leaves a strip of the desktop showing under it.
    setsid nwg-drawer -ovl -mt 30 -nofs -closebtn right \
        -is $((96 * scale)) -c 7 -spacing $((24 * scale)) \
        >/dev/null 2>&1 </dev/null &
}

hide() {
    pkill -x nwg-drawer >/dev/null 2>&1

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
# The drawer builds its grid once, when it starts. The launcher opened when the
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
