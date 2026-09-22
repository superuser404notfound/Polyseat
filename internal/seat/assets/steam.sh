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

# polyseat-steam            make sure the seat has its Steam, in gamescope
# polyseat-steam bigpicture the same, and then ask it for Big Picture
#
# The second form is what the Moonlight entry runs, and it exists because the
# entry used to run `steam steam://open/bigpicture` directly. That command does
# not only open Big Picture: with no Steam running it starts one, and the one it
# starts is outside gamescope, which is the single arrangement where the in-game
# overlay does not work. Closing Big Picture when a stream ends took Steam and
# gamescope with it, so the next connection hit exactly that - a cold Steam in
# the wrong place, Big Picture in the corner, no overlay. Reported from a
# television twice before the log showed `steam.sh` as the parent.
#
# Now the entry cannot start Steam at all. It asks for the pair and then for
# the window.
want_bigpicture=0

if [ "$1" = bigpicture ]; then
    want_bigpicture=1
fi

open_bigpicture() {
    [ "$want_bigpicture" = 1 ] || return 0

    say "opening Big Picture"

    setsid steam steam://open/bigpicture >/dev/null 2>&1 </dev/null &
}

# What decides is gamescope, not Steam, and getting that the wrong way round
# cost a morning.
#
# gamescope is started with setsid, so it has a session of its own and does not
# die with sway - but its Wayland connection does, so a session restart leaves
# gamescope gone and **Steam still running**, detached and talking to whatever X
# server it can find. A guard that asks "is Steam running" then says yes and
# does nothing, and what is left is the arrangement this file exists to avoid:
# Steam outside gamescope, no in-game overlay, Big Picture back in the corner.
# Reported from a television as exactly that.
#
# So gamescope answers whether there is anything to do, and a Steam without one
# is not a Steam to keep.
if pgrep -x gamescope-wl >/dev/null 2>&1; then
    say "gamescope is already running, so nothing was started"
    open_bigpicture

    exit 0
fi

if pgrep -x steam >/dev/null 2>&1; then
    say "Steam is running outside gamescope, where the overlay does not work;"
    say "  closing it so that it can be started inside one"

    steam -shutdown >/dev/null 2>&1

    gone=0

    for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 22 23 24 25; do
        if ! pgrep -x steam >/dev/null 2>&1; then
            gone=1

            break
        fi

        sleep 1
    done

    if [ "$gone" = 0 ]; then
        say "! Steam would not close, so gamescope was not started"
        open_bigpicture

        exit 0
    fi
fi

if [ -z "$SWAYSOCK" ] || [ ! -S "$SWAYSOCK" ]; then
    SWAYSOCK=$(ls -t "$XDG_RUNTIME_DIR"/sway-ipc.* 2>/dev/null | head -1)
    export SWAYSOCK
fi

# The session has to be up before gamescope is started, and at session start
# this script is one of the first things sway runs.
#
# It matters because a gamescope that starts too early dies, and what is left
# behind looks like success: Steam is running, so nothing retries, and it is
# running on the session's own X server rather than inside gamescope. Which
# means no overlay and Big Picture back in the corner - the two bugs this whole
# arrangement exists to fix, reported from a television before it was
# understood. Waiting for the output to have a mode is the cheapest proof that
# sway is ready to be nested in.
# Both waits are overridable so that a test can drive the whole script,
# including the retry, without sitting out half a minute. A seat never sets
# either.
: "${POLYSEAT_SESSION_WAIT:=15}"
: "${POLYSEAT_GAMESCOPE_SETTLE:=20}"

ready=0
tries=0

while [ "$tries" -lt "$POLYSEAT_SESSION_WAIT" ]; do
    tries=$((tries + 1))

    if swaymsg -t get_outputs 2>/dev/null | grep -q '"current_mode"'; then
        ready=1

        break
    fi

    sleep 1
done

if [ "$ready" = 0 ]; then
    say "the session never came up, so Steam was not started"

    exit 0
fi

# Nothing is asked for before there is something to ask. Big Picture is opened
# at the end of this script, once the pair is up.

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
w=$1
h=$2
r=$3

say "starting Steam in gamescope at ${w}x${h}@${r}"

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
# gamescope's own output is kept, overwritten on every start. It is the only log
# in this chain that belongs to us, and a gamescope that does not survive the
# session start is invisible without it - which is exactly what happened, twice,
# before this line existed.
LOG=$HOME/.local/share/polyseat/gamescope.log
mkdir -p "$(dirname "$LOG")" 2>/dev/null

start() {
    setsid gamescope --backend wayland -W "$1" -H "$2" -r "$3" -f -e \
        --xwayland-count 2 -- "$CAPPED" steam -silent >"$LOG" 2>&1 </dev/null &
}

start "$w" "$h" "$r"

# And checked, because the failure is silent and expensive. gamescope takes a
# few seconds to have a process of its own; if it is gone after that, the
# session was not as ready as the output claimed, and one more attempt costs
# nothing. Steam is shut down first so that the second gamescope is not handed
# a client that is already running somewhere else.
sleep "$POLYSEAT_GAMESCOPE_SETTLE"

if ! pgrep -x gamescope-wl >/dev/null 2>&1; then
    say "gamescope did not come up, trying once more"
    say "  its log is at $LOG"

    steam -shutdown >/dev/null 2>&1
    sleep 6
    mv -f "$LOG" "$LOG.first" 2>/dev/null
    start "$w" "$h" "$r"
fi

# Steam needs a moment inside gamescope before it answers a url, and a request
# that arrives too early is the very thing this script exists to prevent: the
# `steam` command would start one of its own.
if [ "$want_bigpicture" = 1 ]; then
    for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 22 23 24 25; do
        pgrep -x steam >/dev/null 2>&1 && break

        sleep 1
    done

    open_bigpicture
fi

exit 0
