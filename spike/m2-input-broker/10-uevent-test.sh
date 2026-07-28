#!/usr/bin/env bash
# The question that decides the whole broker design:
# does a device attached through `incus config device add` reach the
# container's udev as an event?
#
# If it does, libinput (and therefore sway) and SDL see the device with no
# tricks at all, and the fake-udev shim is unnecessary. If it does not, sway
# needs a substitute path.
set -uo pipefail
source "$(dirname "$0")/lib.sh"

PADNAME="polyseat:uevent-test"

cleanup() {
    incus exec "$CT" -- pkill -f 'udevadm monitor' 2>/dev/null
    pkill -f 'padgen.py --seat uevent-test' 2>/dev/null
    for d in $(incus config device list "$CT" 2>/dev/null | grep '^pad-'); do
        incus config device remove "$CT" "$d" >/dev/null 2>&1
    done
}
trap cleanup EXIT

step "Starting the udev monitor inside the container"
incus exec "$CT" -- sh -c \
    'nohup udevadm monitor --udev --kernel --subsystem-match=input > /root/uevents.log 2>&1 &'
sleep 2

step "Creating a device on the host"
"$HERE/../m0-input/padgen.py" --seat uevent-test --quiet > /tmp/padgen-m2.log 2>&1 &
sleep 3

node=""
for d in /sys/class/input/event*; do
    [[ -r "$d/device/name" ]] || continue
    [[ "$(<"$d/device/name")" == "$PADNAME"* ]] && node="${d##*/}"
done
[[ -n "$node" ]] || { bad "device not found on the host"; exit 1; }
ok "host node: /dev/input/$node"

step "Attaching it to the running seat"
incus config device add "$CT" "pad-$node" unix-char \
    source="/dev/input/$node" path="/dev/input/$node" required=false
sleep 4

step "Did the container see an event?"
events=$(incus exec "$CT" -- cat /root/uevents.log 2>/dev/null | grep -c "$node" || true)
incus exec "$CT" -- cat /root/uevents.log 2>/dev/null | tail -20

if (( events > 0 )); then
    ok "YES: $events event(s) for $node. No fake-udev needed."
else
    bad "NO: no event. sway/libinput will not notice the device."
fi

step "Does the container's udev know the device at all?"
incus exec "$CT" -- udevadm info -q property -n "/dev/input/$node" 2>&1 \
    | grep -E '^(DEVNAME|ID_INPUT|MAJOR)' | head -6 \
    || warn "udevadm info does not know it"

step "Does libinput inside the container see it?"
incus exec "$CT" -- sh -c 'command -v libinput >/dev/null || pacman -S --noconfirm --needed libinput >/dev/null 2>&1
    libinput list-devices 2>/dev/null | grep -A1 -i polyseat | head -6' \
    || warn "libinput does not list it"
