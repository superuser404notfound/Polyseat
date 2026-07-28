#!/usr/bin/env bash
# Sub-question: what is the container missing, the event or the udev database?
#
# libudev builds device properties not from /sys but from its database under
# /run/udev/data/. That database never comes into existence inside the container
# because no uevent ever arrives. If copying the host's database entry is enough
# for libinput to see the device, the problem splits into two independent
# halves, and only the hotplug notification needs real netlink work.
#
# The device name deliberately is NOT "polyseat:*", so that the host's hide rule
# does not strip the properties and we get a realistic database entry.
set -uo pipefail
source "$(dirname "$0")/lib.sh"

NAME="m2probe Virtual Gamepad"

cleanup() {
    pkill -f 'padgen.py' 2>/dev/null
    for d in $(incus config device list "$CT" 2>/dev/null | grep '^pad-'); do
        incus config device remove "$CT" "$d" >/dev/null 2>&1
    done
}
trap cleanup EXIT

step "Creating a device on the host"
"$HERE/../m0-input/padgen.py" --name "$NAME" --quiet >/dev/null 2>&1 &
sleep 3
node=""
for d in /sys/class/input/event*; do
    [[ -r "$d/device/name" ]] || continue
    [[ "$(<"$d/device/name")" == "$NAME" ]] && node="${d##*/}"
done
[[ -n "$node" ]] || { bad "device not found"; exit 1; }
minor=$(cut -d: -f2 "/sys/class/input/$node/dev")
ok "/dev/input/$node  (c13:$minor)"

step "The host's database entry"
sudo cat "/run/udev/data/c13:$minor" | sed 's/^/  /'

step "Attaching the node to the seat"
incus config device add "$CT" "pad-$node" unix-char \
    source="/dev/input/$node" path="/dev/input/$node" required=false >/dev/null
sleep 2

step "BEFORE: does libinput inside the container see anything?"
incus exec "$CT" -- sh -c "libinput list-devices 2>/dev/null | grep -c 'm2probe'" \
    | sed 's/^/  matches: /'

step "Copying the database entry into the container"
sudo cat "/run/udev/data/c13:$minor" | \
    incus exec "$CT" -- sh -c "mkdir -p /run/udev/data && cat > /run/udev/data/c13:$minor"
ok "copied"

step "AFTER: udevadm info inside the container"
incus exec "$CT" -- udevadm info -q property -n "/dev/input/$node" 2>&1 \
    | grep -E '^(DEVNAME|ID_INPUT|MAJOR)' | sed 's/^/  /'

step "AFTER: libinput inside the container"
if incus exec "$CT" -- sh -c "libinput list-devices 2>/dev/null | grep -q 'm2probe'"; then
    ok "libinput sees the device: the database alone is enough for enumeration"
    incus exec "$CT" -- sh -c "libinput list-devices 2>/dev/null | grep -A2 'm2probe'" | sed 's/^/  /'
else
    bad "libinput still does not see it"
fi
