#!/bin/sh
# Polyseat - have Steam already running, in gamescope, when somebody sits down.
#
# Started by the session and also by the daemon, which is the whole reason this
# is a script rather than a line in the sway configuration. The daemon closes an
# idle Steam when it has to change something Steam has already read, and it has
# to be able to put back exactly what the session would have started.
#
# ------------------------------------------------------------------ gamescope
#
# Steam runs inside gamescope, and that is not a preference. **The in-game
# overlay does not work under rootless Xwayland.** Steam hands the overlay to a
# game through its own compositor, that compositor needs
# GLX_EXT_texture_from_pixmap, and NVIDIA's GLX client does not offer that
# extension against an X server it did not write - which is what Xwayland is.
# Steam says so itself and then falls back:
#
#     Error: ThreadInit: GLX_EXT_texture_from_pixmap extension unavailable
#     Error: Run: failed to initialize GL thread
#     SP BPM_uid0: Failed to create output window. Falling back to system composer
#
# The fallback pushes a screen sized texture through main memory once per frame.
# Measured on 2026-09-21: the overlay's own page renders at 60 fps, read from
# inside it through Steam's CEF debugging port, and the player sees one or two.
# The game meanwhile is untouched, 16.7 ms per frame with no outlier, so nothing
# about the cap, the present mode or the resolution was ever the cause, and all
# three were tried.
#
# In gamescope that path does not exist. It is also not a workaround: gamescope
# is what a Steam Deck runs, so Steam and its overlay inside it is the one
# arrangement Valve actually tests.
#
# -e is what turns the Steam integration on, and without it the overlay is not
# merely slow, it never appears at all. Valve's own session adds
# --xwayland-count 2 with STEAM_MULTIPLE_XWAYLANDS=1, which is what lets a
# keyboard and a mouse work inside game mode.
#
# The size is the seat's screen at this moment rather than a fixed number.
# gamescope follows the window it was given: polyseat-resize changes the output
# per connected client, sway resizes this window with it, and gamescope renders
# at the new size. Confirmed by changing the output underneath a running one.
#
# ---------------------------------------------------------------------- silent
#
# Summed PSS in two seats here: a client with no Big Picture open is 16
# processes and 732 MB, with it open 18 and 1324 MB. In gamescope Steam comes up
# in Big Picture by design, so that is what a seat pays; what is saved is the
# wait, because a cold Steam takes 15 to 25 seconds before it shows anything and
# those seconds used to be spent in front of somebody who had just picked a game.
#
# Never fails, and never waits. It is an exec line in a session that has other
# things to start, and a Steam that cannot start is a seat that still streams.

: "${XDG_RUNTIME_DIR:=/run/user/$(id -u)}"
: "${DISPLAY:=:0}"
export XDG_RUNTIME_DIR DISPLAY

say() { echo "polyseat-steam: $*" >&2; }

# Asked rather than assumed, because the daemon calls this after it has closed
# one and the session calls it at startup. Either process being there means the
# seat already has its Steam.
if pgrep -x steam >/dev/null 2>&1 || pgrep -x gamescope-wl >/dev/null 2>&1; then
    say "Steam is already running, so nothing was started"

    exit 0
fi

if [ -z "$SWAYSOCK" ] || [ ! -S "$SWAYSOCK" ]; then
    SWAYSOCK=$(ls -t "$XDG_RUNTIME_DIR"/sway-ipc.* 2>/dev/null | head -1)
    export SWAYSOCK
fi

# The screen as it is right now. Only the starting size: gamescope follows the
# window afterwards, so a client that connects later and changes the output
# takes this along with it.
size=$(swaymsg -t get_outputs 2>/dev/null | python3 -c '
import json, sys

try:
    for out in json.load(sys.stdin):
        mode = out.get("current_mode") or {}
        w, h, r = mode.get("width"), mode.get("height"), mode.get("refresh")
        if w and h:
            print(w, h, round((r or 60000) / 1000))
            break
except Exception:
    pass
' 2>/dev/null)

# A seat whose session is not up yet has no output to ask. Starting blind with a
# sensible size is better than not starting: gamescope resizes anyway.
[ -n "$size" ] || size="1920 1080 60"

# shellcheck disable=SC2086
set -- $size

say "starting Steam in gamescope at ${1}x${2}@${3}"

# Overridable so that a test can run the real polyseat-capped from a temporary
# directory and check that what reaches Steam actually carries the cap. The
# default is the only path a seat ever uses.
CAPPED=${POLYSEAT_CAPPED:-/usr/local/bin/polyseat-capped}

STEAM_MULTIPLE_XWAYLANDS=1
export STEAM_MULTIPLE_XWAYLANDS

# The Wayland backend rather than the automatic choice, and it decides two
# things at once. With DISPLAY set, gamescope picks X11 and its own window in
# the session is an Xwayland window with a class and no app_id, which is why the
# rule this file's session configuration has carried since the beginning,
# for_window [app_id="^gamescope$"], never once matched. As a Wayland window it
# matches, so the workspace assignment works. And it is one X server less in a
# chain that already has enough of them.
# Silent, and deliberately not -gamepadui. That flag puts Steam into the Deck's
# session mode, where "switch to desktop" is a button that sends
# CSteamOSManager_SwitchToDesktop_Request to a SteamOS service. There is no such
# service here, so the request never answers and Big Picture hangs on that
# screen - seen on a television before it was understood. The overlay works
# because of gamescope, not because of that flag. Big Picture is opened the way
# it always was, by the application entry, and the two workspaces are what a
# player switches between.
setsid gamescope --backend wayland -W "$1" -H "$2" -r "$3" -f -e --xwayland-count 2 \
    -- "$CAPPED" steam -silent >/dev/null 2>&1 </dev/null &

exit 0
