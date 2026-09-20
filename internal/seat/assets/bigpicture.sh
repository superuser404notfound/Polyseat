#!/bin/sh
# Polyseat - start Steam Big Picture and make sure it fills the screen.
#
# Two things stand between picking Big Picture in Moonlight and Big Picture
# owning the screen, and neither of them is Steam misbehaving on purpose.
#
# The first is the rule in the sway configuration. It matches on the window's
# title, and a cold Steam maps its window before it has that title, so the rule
# sees something called "Steam" and never fires. Warm, the title is there in
# time and it works. The title is matched as a fragment because Steam
# translates it: English says "Steam Big Picture Mode", German
# "Big-Picture-Modus". The sway configuration and polyseat-bigpicture-watch
# carry the same pattern, and a test holds the three to it.
#
# So the rule stays as the fast path and the loop below insists afterwards.
# Asking for fullscreen on a window that already has it does nothing, and
# asking for it on a window that does not exist yet does nothing either, so the
# only cost of being wrong there is a few seconds of a loop.
#
# The second is the one that had this reported from the couch three times:
# fullscreen is not the same thing as a picture that fills the screen.
#
#     container id=6   rect 2250x1206   geometry 1280x800   fullscreen_mode 1
#
# Big Picture is not a window that resizes. It is a fixed 1280x800 interface
# that Steam scales to whatever its window happens to be, and Steam's own log
# says how that goes wrong. The window is created hidden, at Steam's own
# default size and at no position at all, and when Steam shows it, it asks
# once how big the window has become and scales to that:
#
#     Created window: size: 1280,800 pos: 805240832,805240832
#     WasHidden 1: (0, 0) 1280x800
#     ThreadSetForceDeviceScaleFactors 1.000000 * 1.000000 = 1.000000
#     WasHidden 0: (0, 0) 1280x800
#     ThreadSetForceDeviceScaleFactors 1.000000 * 1.423025 = 1.420000
#
# That last line is sway having made the window fullscreen before Steam asked.
# When it is missing, Steam scales a 1280x800 interface by one into a window
# that owns the whole screen, and the rest is black. It never asks again.
#
# Which of the two happens is a race between sway's rule, which can only fire
# once the window is mapped and titled, and Steam's one question. Nine starts
# in one seat on one day, each matched to the Steam process that opened it:
#
#     into a client that was already running    4 starts, 4 came up right
#     starting a client of its own              5 starts, 1 came up right
#
# which is why restarting Big Picture looks like a cure. It is not a promise
# that a warm one always wins - it is the same race, and four is not many - and
# the factor below makes the question moot, because it says which way the race
# went rather than guessing from how Steam was started.
#
# What is true is that Steam does react to a change of size afterwards. It is
# already fullscreen, so there is nothing for sway to change; taking fullscreen
# off and putting it back is the smallest change there is, and it is what a
# player does by hand when they restart Big Picture and it "fixes itself".
#
# So the question is only ever whether Steam has the right factor, and Steam
# answers it in writing. Version 0.22.0 photographed the screen instead and was
# silently wrong twice in one seat - it cannot photograph a width that is not a
# multiple of four, and it read the seat's own background as a painted picture.
# A number Steam wrote down about itself is a better witness than a screenshot
# of what it drew.
#
# Steam falls back to its system composer in a seat, because XWayland's GLX has
# no GLX_EXT_texture_from_pixmap and its own GL compositor will not start
# without it. Whether the path that does work is the reason it only asks once
# is not something this can find out; it is the first place to look if this
# ever has to go away.

: "${XDG_RUNTIME_DIR:=/run/user/$(id -u)}"
: "${HOME:=/home/player}"
export XDG_RUNTIME_DIR

say() { echo "polyseat-bigpicture: $*" >&2; }

LOG=$HOME/.local/share/Steam/logs/webhelper.txt
MATCH='[class="^steam$" title="[Bb]ig[ _-][Pp]icture"]'

# How long to let Steam write its line before reading it, and how long to wait
# after an off and on before reading it again. Steam writes the factor in the
# same second as it shows or resizes the window, so these are margin, not
# measurement, and the margin is small on purpose: reading too early answers
# "unknown", and "unknown" is one off and on done blind, which is what would
# have happened anyway. A player watching Big Picture come up small waits this
# long, so the seconds here are the ones they see.
SETTLE=1
AFTER=2

# How long the window is left windowed between the two halves of an off and on.
# Long enough to be a change of its own rather than one sway folds into the
# next, short enough not to read as a flicker.
OFF=0.4

# How many times to insist. One is what it took every time this was reproduced;
# the second is for a Steam that was still finding its feet at the first.
TRIES=2

if [ -z "$SWAYSOCK" ] || [ ! -S "$SWAYSOCK" ]; then
    SWAYSOCK=$(ls -t "$XDG_RUNTIME_DIR"/sway-ipc.* 2>/dev/null | head -1)
    export SWAYSOCK
fi

# Everything written in Steam's log before this starts. Only what comes after
# it is about the window this is about, and a log that has been rotated away
# under us comes back as nothing, which reads as "cannot tell" and is handled.
mark=$(wc -l < "$LOG" 2>/dev/null || echo 0)
mark=$((mark + 1))

setsid steam steam://open/bigpicture >/dev/null 2>&1 </dev/null &

[ -S "$SWAYSOCK" ] || exit 0

# Where Big Picture is: "absent", "windowed", or "full" and the size of the
# window sway gave it.
state() {
    swaymsg -t get_tree 2>/dev/null | python3 -c '
import json, re, sys

want = re.compile(r"[Bb]ig[ _-][Pp]icture")


def mine(node):
    if not node.get("pid"):
        return False

    if (node.get("window_properties") or {}).get("class") != "steam":
        return False

    return bool(want.search(node.get("name") or ""))


def walk(node):
    if mine(node):
        if not node.get("fullscreen_mode"):
            return "windowed"

        rect = node.get("rect") or {}

        return "full %d %d" % (rect.get("width") or 0, rect.get("height") or 0)

    for key in ("nodes", "floating_nodes"):
        for child in node.get(key, []):
            found = walk(child)
            if found:
                return found

    return None


try:
    print(walk(json.load(sys.stdin)) or "absent")
except Exception:
    print("absent")
' 2>/dev/null
}

# What Steam last said about its own scaling, against what it would have to be:
# "ok", "wrong <what it is> <what it should be>", or "unknown".
#
# Big Picture is a fixed 1280x800 interface, so filling a window of w by h takes
# the geometric mean of the two ratios, and that is exactly the number Steam
# writes. Two readings from two seats, to six places, both exact:
#
#     2250x1206 -> 1.627852      1920x1080 -> 1.423025
#
# "unknown" is not "wrong". A Steam that has not written the line yet, or one
# that has stopped writing it, is a reason to say so and act blind once, not a
# reason to insist against nothing.
scaling() {
    tail -n +"$mark" "$LOG" 2>/dev/null | python3 -c '
import math, sys

wide, high = float(sys.argv[1]), float(sys.argv[2])
seen = None

for line in sys.stdin:
    if "ThreadSetForceDeviceScaleFactors" not in line:
        continue

    parts = line.split()

    try:
        seen = float(parts[parts.index("*") + 1])
    except (ValueError, IndexError):
        continue

if seen is None or wide <= 0 or high <= 0:
    print("unknown")
else:
    want = math.sqrt(wide / 1280 * high / 800)
    print("ok" if abs(seen - want) <= 0.01 * want
          else f"wrong {seen:.6f} {want:.6f}")
' "$1" "$2" 2>/dev/null
}

# One off and on. The caller has already read the tree, so this only does it.
nudge() {
    swaymsg -- "$MATCH" fullscreen disable >/dev/null 2>&1
    sleep "$OFF"
    swaymsg -- "$MATCH" fullscreen enable >/dev/null 2>&1
    sleep "$AFTER"
}

# Long enough for a cold Steam, which unpacks and updates itself before it
# shows anything.
i=0
while [ "$i" -lt 60 ]; do
    sleep 2
    i=$((i + 1))

    # Split on purpose: these answer with a word and sometimes two numbers
    # after it, and the positional parameters are the one place a POSIX shell
    # can hold all three without a temporary file.
    # shellcheck disable=SC2046
    set -- $(state)

    case "$1" in
        windowed)
            swaymsg -- "$MATCH" fullscreen enable >/dev/null 2>&1
            continue
            ;;
        full) ;;
        *) continue ;;
    esac

    sleep "$SETTLE"

    tries=0

    while :; do
        # Read again every time round. The player may have left Big Picture or
        # started a game from it, and pulling the screen off a game and handing
        # it back is worse than the corner this is here to fix.
        # shellcheck disable=SC2046
        set -- $(state)

        if [ "$1" != full ]; then
            say "Big Picture no longer has the screen, so it was left alone"
            exit 0
        fi

        # shellcheck disable=SC2046
        set -- $(scaling "$2" "$3") "$2" "$3"

        case "$1" in
            ok)
                if [ "$tries" -gt 0 ]; then
                    say "Big Picture fills the screen after $tries off and on"
                else
                    say "Big Picture came up at the size of the screen"
                fi

                exit 0
                ;;
            unknown)
                if [ "$tries" -gt 0 ]; then
                    say "Steam still says nothing about its own scaling," \
                        "so it is left as it is"
                    exit 0
                fi

                say "Steam says nothing about its own scaling, so Big Picture" \
                    "gets one off and on in case it needs it"
                ;;
            wrong)
                if [ "$tries" -ge "$TRIES" ]; then
                    say "Steam is still drawing at $2 where $3 fills the" \
                        "screen, after $tries tries, so it is left as it is"
                    exit 0
                fi

                say "Steam is drawing at $2 where $3 fills the screen," \
                    "so Big Picture gets an off and on"
                ;;
            *)
                # Neither of the three, which means the reading itself failed.
                # Nothing here is worth flashing somebody's screen over.
                say "could not tell what Steam thinks its scaling is," \
                    "so Big Picture was left as it is"
                exit 0
                ;;
        esac

        nudge
        tries=$((tries + 1))
    done
done

say "Big Picture never had the screen in two minutes, so it was left alone"

exit 0
