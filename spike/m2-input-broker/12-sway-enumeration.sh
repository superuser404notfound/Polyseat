#!/usr/bin/env bash
# Asks the real consumer: does sway see an attached device if it is already
# there when sway starts? And does the copied udev database entry contribute
# anything?
#
# Two runs, so the two factors can be judged separately:
#   A) the device node only
#   B) node plus database entry from the host
#
# CAUTION: restarts the sway session, so a running Moonlight stream drops.
set -uo pipefail
source "$(dirname "$0")/lib.sh"

NAME="m2probe Virtual Gamepad"
PLAYER=player

as_player() {
    incus exec "$CT" -- sudo -u "$PLAYER" env \
        XDG_RUNTIME_DIR=/run/user/1000 \
        DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus \
        SWAYSOCK="$(incus exec "$CT" -- sh -c \
            'ls -t /run/user/1000/sway-ipc.*.sock 2>/dev/null | head -1')" "$@"
}

cleanup() {
    pkill -f 'padgen.py' 2>/dev/null
    for d in $(incus config device list "$CT" 2>/dev/null | grep '^pad-'); do
        incus config device remove "$CT" "$d" >/dev/null 2>&1
    done
}
trap cleanup EXIT

count_inputs() {
    as_player swaymsg -t get_inputs 2>/dev/null | grep -c '"identifier"' || true
}
show_inputs() {
    as_player swaymsg -t get_inputs 2>/dev/null \
        | grep -E '"identifier"|"name"' | head -10 | sed 's/^/    /'
}

step "Baseline: input devices in sway"
echo "  count: $(count_inputs)"

step "Creating a device on the host and attaching it"
"$HERE/../m0-input/padgen.py" --name "$NAME" --quiet >/dev/null 2>&1 &
sleep 3
node=""
for d in /sys/class/input/event*; do
    [[ -r "$d/device/name" ]] || continue
    [[ "$(<"$d/device/name")" == "$NAME" ]] && node="${d##*/}"
done
[[ -n "$node" ]] || { bad "device not found"; exit 1; }
minor=$(cut -d: -f2 "/sys/class/input/$node/dev")
ok "/dev/input/$node (c13:$minor)"
incus config device add "$CT" "pad-$node" unix-char \
    source="/dev/input/$node" path="/dev/input/$node" mode=0666 required=false >/dev/null

step "Run A: node only, restart sway"
incus exec "$CT" -- rm -f "/run/udev/data/c13:$minor"
as_player systemctl --user restart polyseat-sway.service
sleep 10
a=$(count_inputs)
echo "  input devices: $a"
show_inputs

step "Run B: plus the udev database entry"
sudo cat "/run/udev/data/c13:$minor" | \
    incus exec "$CT" -- sh -c "mkdir -p /run/udev/data && cat > /run/udev/data/c13:$minor"
as_player systemctl --user restart polyseat-sway.service
sleep 10
b=$(count_inputs)
echo "  input devices: $b"
show_inputs

step "Interpretation"
if   (( a > 0 )); then ok  "sway sees the device even without a database entry."
elif (( b > 0 )); then ok  "sway sees it ONLY with a database entry: the broker has to write one."
else                   bad "sway sees it in neither case. libinput needs more than /sys plus the node."
fi
