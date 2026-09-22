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
# The session starts the pair and stops there. Big Picture is built when
# somebody asks for it, which is what picking Steam in Moonlight does.
#
# Both halves of that are deliberate. Starting Steam early is what takes the
# cold start out of the evening: a Steam that has never run needs fifteen to
# twenty-five seconds to be able to answer anything at all, and those seconds
# used to be spent in front of somebody who had just picked a game.
#
# Leaving Big Picture closed is what keeps an idle seat cheap, and the price of
# it is small. Measured in seat vince on 2026-09-22, the same Steam throughout
# and ninety seconds of settling on each side:
#
#     still         1386 MB of memory, 493 MB of video memory
#     Big Picture   1411 MB of memory, 711 MB of video memory
#
# So the window costs 25 MB of memory and 218 MB of video memory. The memory is
# nothing, and the video memory is not: two seats share one card here, and a
# game wants every megabyte of it that an empty menu is holding.
#
# What it costs to build on a Steam that is already warm was measured three
# times in Sunshine's own command order: 522, 616 and 607 milliseconds. On a
# Steam that is not running at all it is the whole cold start, which is the
# thing the early start exists to have done already.
#
# Going back the other way is not possible, which is worth writing down because
# it looks like it should be. Once Big Picture has been drawn, those 218 MB
# belong to the renderer and not to the window, and closing the window does not
# return them. Measured in seat vince on 2026-09-22:
#
#     steam://close/bigpicture      Steam shows its desktop window instead and
#                                   the seat holds 791 MB, more than before
#     closing the window from
#     outside, as a window manager
#     would                         the screen is empty, the renderer keeps its
#                                   surface, 221 MB of it
#     SteamClient.UI.ExitBigPictureMode
#     through the debugging port    frees it, and leaves a Steam that will not
#                                   open Big Picture again
#     steam://close/steam           quits Steam
#
# So the only way back to a cheap seat is a restart, which is what refresh does
# and what picking Desktop in Moonlight already runs.
#
# Never fails. It is an exec line in a session that has other things to start,
# and a Steam that cannot start is a seat that still streams.
#
# It does wait, and the waiting is the point: it sits there until sway can see
# Big Picture, so that the player who picks Steam in Moonlight finds a window
# rather than starting one. Nobody is watching while it does, which is the whole
# reason it happens at session start.

: "${XDG_RUNTIME_DIR:=/run/user/$(id -u)}"
: "${DISPLAY:=:0}"
export XDG_RUNTIME_DIR DISPLAY

say() { echo "polyseat-steam: $*" >&2; }

# gamescope's own output is kept, overwritten on every start. It is the only log
# in this chain that belongs to us, and a gamescope that does not survive the
# session start is invisible without it - which is exactly what happened, twice,
# before this line existed. Named up here because everything below may have to
# point somebody at it.
LOG=$HOME/.local/share/polyseat/gamescope.log
mkdir -p "$(dirname "$LOG")" 2>/dev/null

# And one of these at a time, whatever starts them.
#
# The entries overlap in ordinary use: picking Desktop starts a refresh that
# takes half a minute, and picking Steam a second later used to find no
# gamescope, start a second one, and hand it a Steam the first was already
# building. The lock makes the second run wait for the first and then ask its
# questions again, by which time they have different answers.
#
# Held on a file descriptor this script owns rather than by handing the whole
# script to flock, and that is not style. A lock lives on the open file, so
# every process that inherits the descriptor holds it too - and this script's
# whole purpose is to start a gamescope that outlives it. The first version did
# hand itself to flock, and gamescope inherited the lock and kept it for as long
# as it ran, so the next run of this script waited for ever. Measured rather
# than reasoned about: a second run sat there for seven minutes with nothing in
# its log. The two commands that outlive this script therefore close the
# descriptor on their way out, which is what 9>&- says.
#
# The wait is bounded for the same reason. A lock that somehow never comes free
# must not turn picking Steam in Moonlight into a button that does nothing at
# all; two minutes is longer than the slowest thing this script does.
if command -v flock >/dev/null 2>&1 && [ -w "$XDG_RUNTIME_DIR" ]; then
    exec 9>"$XDG_RUNTIME_DIR/polyseat-steam.lock"

    flock -w 120 9 ||
        say "! another polyseat-steam held the lock for two minutes; going ahead"
fi

# Whether the caller wants a window or only a Steam.
#
# The session wants only a Steam: the window is 218 MB of video memory in a seat
# nobody is sitting at, and about six hundred milliseconds to build once Steam
# is warm. Moonlight's Steam entry wants the window, because that is what the
# player just asked for.
want_window=0
[ "$1" = bigpicture ] && want_window=1

# The session's socket, resolved here rather than inherited, because the daemon
# runs this script through incus exec, where there is no session environment to
# inherit one from. Everything below asks sway something, so this comes first.
if [ -z "$SWAYSOCK" ] || [ ! -S "$SWAYSOCK" ]; then
    SWAYSOCK=$(ls -t "$XDG_RUNTIME_DIR"/sway-ipc.* 2>/dev/null | head -1)
    export SWAYSOCK
fi

# And the display itself, for the same reason and with a far worse failure.
#
# gamescope is started with --backend wayland and needs this to find the
# compositor it nests in. Without it, it does not stop: it falls back to X11 and
# comes up on the session's own X server, where sway sees an Xwayland window
# with a class and no app_id. Every rule this session has for gamescope matches
# on app_id, so that window is never assigned to its workspace, never made
# fullscreen, and never seen by the check that waits for Big Picture. One line
# in gamescope's log is all it says about it:
#
#     Error: xdg_backend: Couldn't connect to Wayland display.
#
# Measured in seat vince on 2026-09-22, in a run started the way the daemon
# starts one: through incus exec, where nothing of the session's environment
# arrives. The session's own runs inherit the variable and were never affected,
# which is how this went out unnoticed.
if [ -z "$WAYLAND_DISPLAY" ] || [ ! -S "$XDG_RUNTIME_DIR/$WAYLAND_DISPLAY" ]; then
    # The newest socket, the way SWAYSOCK is found above. The digits are spelled
    # out rather than globbed with a star so that the lock file the compositor
    # keeps beside each socket is not a candidate. The variable holds the name,
    # not the path.
    WAYLAND_DISPLAY=$(ls -t "$XDG_RUNTIME_DIR"/wayland-[0-9] \
        "$XDG_RUNTIME_DIR"/wayland-[0-9][0-9] 2>/dev/null | head -1)
    WAYLAND_DISPLAY=${WAYLAND_DISPLAY##*/}
    export WAYLAND_DISPLAY
fi

if [ -z "$WAYLAND_DISPLAY" ]; then
    say "! no Wayland display found. gamescope will come up on X11, and sway"
    say "  will not recognise the window it makes"
fi

# This script always ends with Big Picture open, and that is the point of it.
#
# The Moonlight entry runs it too, rather than `steam steam://open/bigpicture`.
# That command does not only open Big Picture: with no Steam running it starts
# one, and the one it starts is outside gamescope, which is the single
# arrangement where the in-game overlay does not work. Reported from a
# television twice before the log showed steam.sh as the parent of the Steam
# that was running.
#
# Opened here at session start as well, and not left to the entry, because a
# Steam that is running is not a Big Picture that is ready: with -silent there
# is no window at all until somebody asks, and building it is the wait the
# autostart was supposed to remove. A player picking Steam Big Picture in
# Moonlight should find it, not start it.
#
#   polyseat-steam              make sure the seat has its Steam, running
#                               silently in gamescope. What the session runs,
#                               and what the daemon runs after closing an idle
#                               one
#   polyseat-steam bigpicture   the same, and then Big Picture on the screen.
#                               What picking Steam in Moonlight runs
#   polyseat-steam refresh      the same as the first, after throwing the
#                               current Big Picture away
#
# refresh is what the Desktop entry runs, and it exists for one screen: Steam
# offers "switch to desktop" under gamescope, has no implementation of it
# outside SteamOS, and waits on that screen for ever. Closing Big Picture is the
# way out of it - but closing it and leaving it closed takes gamescope's only
# window with it, so the workspace is empty and the next player switches to a
# black screen and then waits for Big Picture to be built. Which is exactly what
# was reported.
#
# So it is closed and opened again, while the player is on the other workspace
# and cannot see either.
#
# Any other argument is accepted and treated as the first, so that a seat whose
# application list is one version behind keeps starting Steam.
open_bigpicture() {
    setsid steam steam://open/bigpicture >/dev/null 2>&1 </dev/null 9>&- &
}

# Asked for, then looked at, then asked again.
#
# Readiness is not a process. `steam steam://open/bigpicture` writes into a pipe
# in the home directory, and a Steam that is not listening on it yet does not
# queue the request, it drops it: steam-runtime-steam-remote says "Steam is not
# running" and the request is gone. A cold Steam takes fifteen to twenty-five
# seconds to get that far, and Big Picture itself is slower still.
#
# Asking once after a wait chosen to be about right is what left a seat with a
# Steam running and no Big Picture. The log said "opening Big Picture" twenty
# seconds in and Steam's own log had nothing about any url, so the player picked
# Steam in Moonlight and watched Big Picture being built in front of them -
# which is the twenty to thirty seconds that were reported, and which the
# autostart was supposed to have spent already. Measured in both seats on
# 2026-09-22.
#
# So the window is the answer, not the request. Big Picture is gamescope's only
# window, so sway seeing one is proof that it is up; while sway sees none, the
# request is made again every five seconds. By the time anybody picks Steam in
# Moonlight this has finished, and the entry finds a window rather than building
# one.
#
# Overridable for a test, which has no patience and no Steam. A seat never sets
# it.
: "${POLYSEAT_BIGPICTURE_WAIT:=90}"

show_bigpicture() {
    said_it=0
    waited=0

    while [ "$waited" -lt "$POLYSEAT_BIGPICTURE_WAIT" ]; do
        if mapped; then
            say "Big Picture is up"

            return 0
        fi

        if [ $((waited % 5)) -eq 0 ]; then
            [ "$said_it" = 1 ] || say "opening Big Picture"

            said_it=1

            open_bigpicture
        fi

        waited=$((waited + 1))

        sleep 1
    done

    say "! Big Picture did not open. gamescope's log is at $LOG"

    return 1
}

# When a process started, in clock ticks since the machine came up.
#
# Read out of /proc rather than asked of ps, because the field ps offers is
# whole seconds and the two processes compared below are started within the same
# second of each other. A process name can contain spaces and brackets, so
# everything up to its closing bracket goes first; starttime is then the
# twentieth field of what is left.
started() {
    sed 's/^.*) //' "/proc/$1/stat" 2>/dev/null | cut -d' ' -f20
}

# gamescopes tells this session's gamescope apart from one left behind by a
# session that is gone, and getting that wrong is what made the autostart
# useless.
#
# gamescope is started with setsid so that it keeps its own session, but its
# Wayland connection still dies with sway, and it follows a few seconds later.
# Those few seconds are the trap. A session restart runs this script as one of
# sway's first exec lines, `pgrep -x gamescope-wl` finds the previous session's
# gamescope still breathing, and the script decides there is nothing to do. The
# leftover then dies, the seat has no Steam at all, and whatever the player
# picks first in Moonlight pays the cold start - which is what was reported as
# twenty to thirty seconds after clicking Steam.
#
# The same mistake made the retry below believe it had succeeded. In seat joser
# on 2026-09-22: sway at 07:33:18, "starting Steam in gamescope" in the same
# second, and twenty seconds later "opening Big Picture" rather than "did not
# come up" - while the gamescope actually running was the old one, and the one
# this script had started was already gone. Both seats, same morning.
#
# Age is what says which is which, and it says it exactly: a gamescope that
# already existed before this sway did cannot be nested in it. Nothing else
# about the process carries the answer.
gamescopes() {
    since=0

    sway=$(pgrep -x sway 2>/dev/null | head -1)
    if [ -n "$sway" ]; then
        since=$(started "$sway")
    fi

    # No sway to compare against means no session at all, and this script will
    # say so further down. Until then everything counts as this session's, so
    # that a run which cannot tell takes nothing away.
    [ -n "$since" ] || since=0

    for pid in $(pgrep -x gamescope-wl 2>/dev/null); do
        at=$(started "$pid")
        [ -n "$at" ] || continue

        if [ "$at" -ge "$since" ]; then
            echo "ours $pid"
        else
            echo "stale $pid"
        fi
    done
}

# Whether this session has a gamescope of its own, which is the only question
# the guard below is allowed to ask.
ours() {
    gamescopes | grep -q '^ours '
}

# Whether somebody is playing something.
#
# Everything below that restarts Steam takes a running game with it, and a
# player whose evening ends because the stream dropped and an undo command fired
# would have no idea why. Steam starts every game through its own reaper, whose
# command line carries the app id, so this is a question the process table can
# answer exactly rather than by guessing. The bare name is asked as well,
# because that is the same process under a shorter description and costs
# nothing to check.
playing() {
    pgrep -f "SteamLaunch AppId=" >/dev/null 2>&1 && return 0

    pgrep -x reaper >/dev/null 2>&1
}

# And whether sway can see its window yet, which is the positive proof that
# gamescope came up and is nested in this session. Used to decide how long to
# wait, not whether to retry: a false negative here would start a second
# gamescope, and `ours` cannot have one.
mapped() {
    swaymsg -t get_tree 2>/dev/null | grep -q '"app_id" *: *"gamescope"'
}

# Not while somebody is playing, and this one is a bug that was there from the
# first day refresh existed.
#
# Picking Desktop in Moonlight runs a refresh, and refresh shuts Steam down.
# Under gamescope that takes the running game with it, so a player who left a
# game to look at the desktop for a moment ended it. Nothing said so anywhere.
if [ "$1" = refresh ] && playing; then
    say "a game is running, so Steam was left exactly as it is"

    exit 0
fi

# refresh means starting the pair over, and it has to: closing Big Picture on
# its own is not survivable. It is gamescope's only window, so closing it ends
# gamescope, which takes Steam with it - measured, with gamescope saying so:
#
#     Error: xdg_backend: Failed to dispatch input thread queue: protocol error
#     reaper: Parent of gamescopereaper was killed. Killing children.
#
# What is left is an empty workspace and nothing to open Big Picture in. So the
# shutdown is done deliberately and waited out, and the rest of this script then
# builds the pair again from nothing. It takes half a minute and the player
# spends it on the desktop, which is where they just asked to be.
if [ "$1" = refresh ] && pgrep -x steam >/dev/null 2>&1; then
    say "restarting Steam, so that the next Big Picture is a fresh one"

    steam -shutdown >/dev/null 2>&1

    for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 22 23 24 25; do
        pgrep -x steam >/dev/null 2>&1 || break

        sleep 1
    done

    # gamescope goes when its child does, but not instantly, and a gamescope
    # that is still there when the check below runs would look like success.
    for _ in 1 2 3 4 5 6 7 8 9 10; do
        ours || break

        sleep 1
    done
fi

# What decides is gamescope, not Steam, and getting that the wrong way round
# cost a morning: a session restart leaves gamescope gone and Steam still
# running, detached and talking to whatever X server it can find, and a guard
# that asks "is Steam running" says yes and does nothing. What is left is the
# arrangement this file exists to avoid - Steam outside gamescope, no in-game
# overlay, Big Picture in the corner - and it was reported from a television as
# exactly that. So gamescope answers, and a Steam without one is not a Steam to
# keep.
if ours; then
    if [ "$want_window" = 0 ]; then
        say "Steam is already running in gamescope, so nothing was started"

        exit 0
    fi

    # And if sway can already see a window, there is nothing to ask for either:
    # that window is Big Picture, or the game somebody is playing in it, and a
    # url would take a player out of one of them.
    if mapped; then
        say "Big Picture is already on the screen, so nothing was done"

        exit 0
    fi

    # This is the ordinary path, and the one the whole arrangement is built
    # around: Steam has been warm since the seat started, so what is left is
    # Big Picture drawing itself.
    say "Steam is running; asking it for Big Picture"
    show_bigpicture

    exit 0
fi

# A gamescope that is not this session's is not a gamescope, it is a process
# that has not noticed yet. It cannot be drawn into and it cannot be handed a
# window, so it is taken away rather than waited for: leaving it there is what
# defeated the guard above, and the second or two spent here is what buys the
# autostart back.
if pgrep -x gamescope-wl >/dev/null 2>&1; then
    say "a gamescope from a session that is gone is still here; taking it away"

    # Its Steam first, and politely, because that Steam owns the library this
    # seat shares with the other one.
    steam -shutdown >/dev/null 2>&1

    for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 22 23 24 25; do
        pgrep -x steam >/dev/null 2>&1 || break

        sleep 1
    done

    for pid in $(gamescopes | sed -n 's/^stale //p'); do
        kill "$pid" 2>/dev/null
    done

    for _ in 1 2 3 4 5 6 7 8 9 10; do
        pgrep -x gamescope-wl >/dev/null 2>&1 || break

        sleep 1
    done
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

        # Asked for once and not waited on, because there is no gamescope here
        # to put a window in and nothing for show_bigpicture to look for. It is
        # the best this state can do: a Big Picture outside gamescope, which is
        # better than none while somebody is sitting there.
        [ "$want_window" = 1 ] && open_bigpicture

        exit 0
    fi
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
start() {
    setsid gamescope --backend wayland -W "$1" -H "$2" -r "$3" -f -e \
        --xwayland-count 2 -- "$CAPPED" steam -silent \
        >"$LOG" 2>&1 </dev/null 9>&- &
}

start "$w" "$h" "$r"

# And checked, because the failure is silent and expensive: a gamescope that did
# not come up leaves Steam running on the session's own X server instead, where
# the overlay does not work and Big Picture sits in the corner.
#
# Waited out flat rather than polled, and that is not laziness. A gamescope that
# fails here does not fail at once, it comes up and dies a few seconds later
# when it turns out the session was not ready, so a check that stopped at the
# first sighting would miss exactly the case it exists for. The seconds are free:
# a Steam this cold cannot answer a url yet either, and the loop that waits for
# it starts straight afterwards.
sleep "$POLYSEAT_GAMESCOPE_SETTLE"

# Whether to try again is decided by this session's gamescope and not by any
# gamescope, which is what the old check asked. A leftover from a session that
# is gone answered that question for twenty seconds and made the retry believe
# it had succeeded. Steam is shut down first so that the second gamescope is not
# handed a client that is already running somewhere else.
if ! ours; then
    say "gamescope did not come up, trying once more"
    say "  its log is at $LOG"

    steam -shutdown >/dev/null 2>&1
    sleep 6
    mv -f "$LOG" "$LOG.first" 2>/dev/null
    start "$w" "$h" "$r"
fi

if [ "$want_window" = 1 ]; then
    show_bigpicture

    exit 0
fi

# And otherwise it is left exactly here: a Steam that is warm, in gamescope,
# with no window anywhere. gamescope with a silent Steam presents nothing, so
# sway has no window either and the workspace stays empty until somebody picks
# Steam. That is the state a seat waits in.
say "Steam is up and silent; Big Picture is built when somebody asks for it"

exit 0
