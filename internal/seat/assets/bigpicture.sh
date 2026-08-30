#!/bin/sh
# Polyseat - start Steam Big Picture and make sure it fills the screen.
#
# The sway configuration has a rule for this, and the rule is not enough. It
# matches on the window's title, and a cold Steam maps its window before it has
# that title, so the rule sees something called "Steam" and never fires. Warm,
# the title is there in time and it works, which is why restarting Big Picture
# appeared to fix it: the window came up small in the corner of the screen the
# first time and correctly the second.
#
# The title is matched as a fragment because Steam translates it: English says
# "Steam Big Picture Mode", German "Big-Picture-Modus". The sway configuration
# and polyseat-bigpicture-watch carry the same pattern, and a test holds the
# three to it.
#
# So the rule stays as the fast path and this insists afterwards. Asking for
# fullscreen on a window that already has it does nothing, and asking for it on
# a window that does not exist yet does nothing either, so the only cost of
# being wrong here is a few seconds of a loop.
#
# It stops as soon as the window is fullscreen, rather than running for a fixed
# time, so that leaving Big Picture for the desktop a moment later does not drag
# you back into it.

: "${XDG_RUNTIME_DIR:=/run/user/$(id -u)}"
export XDG_RUNTIME_DIR

if [ -z "$SWAYSOCK" ] || [ ! -S "$SWAYSOCK" ]; then
    SWAYSOCK=$(ls -t "$XDG_RUNTIME_DIR"/sway-ipc.* 2>/dev/null | head -1)
    export SWAYSOCK
fi

setsid steam steam://open/bigpicture >/dev/null 2>&1 </dev/null &

[ -S "$SWAYSOCK" ] || exit 0

# Long enough for a cold Steam, which unpacks and updates itself before it
# shows anything.
i=0
while [ "$i" -lt 60 ]; do
    sleep 2
    i=$((i + 1))

    state=$(swaymsg -t get_tree 2>/dev/null | python3 -c '
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
        return "full" if node.get("fullscreen_mode") else "windowed"

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
' 2>/dev/null)

    case "$state" in
        full) exit 0 ;;
        windowed)
            swaymsg '[class="^steam$" title="[Bb]ig[ _-][Pp]icture"] fullscreen enable' \
                >/dev/null 2>&1
            ;;
    esac
done

exit 0
