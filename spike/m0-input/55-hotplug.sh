#!/usr/bin/env bash
# H7 - hotplug at runtime.
#
# H6 only shows that SDL finds what is already there when it starts. But Steam
# runs permanently inside a seat, and pads only appear once somebody connects.
# Whether that is noticed depends on udev netlink uevents - and those do not
# reach a container.
#
# Prerequisite: 40-inject.sh has run, the first pad is alive.
set -uo pipefail
source "$(dirname "$0")/lib.sh"

step "Starting sdlprobe in watch mode (30 s)"
incus exec "$CT" -- sh -c \
    'cd /root && nohup ./sdlprobe --watch 30 > /root/watch.log 2>&1 &'
sleep 4
incus exec "$CT" -- cat /root/watch.log 2>/dev/null

step "Creating a second pad inside the container"
# Its own vendor:product so SDL does not put its Xbox mapping on top and both
# pads stay distinguishable in the output.
incus exec "$CT" -- sh -c \
    'nohup python3 -u /root/padgen.py --seat m0b --quiet --vendor 0x1234 --product 0x5678 > /root/pad-b.log 2>&1 &'
sleep 3
incus exec "$CT" -- cat /root/pad-b.log

step "Looking for the host node of the second pad"
node=""
for d in /sys/class/input/event*; do
    [[ -r "$d/device/name" ]] || continue
    [[ "$(<"$d/device/name")" == "polyseat:m0b"* ]] && node="${d##*/}"
done
[[ -n "$node" ]] || { bad "second pad not found on the host"; exit 1; }
ok "found: /dev/input/$node"

step "Hotplug into the running container"
incus config device add "$CT" "pad-$node" unix-char \
    source="/dev/input/$node" path="/dev/input/$node" required=false

step "Waiting for the watcher to finish"
sleep 25

step "What did SDL notice?"
incus exec "$CT" -- cat /root/watch.log

cat <<'EOF'

Interpretation:
  2 devices reported -> hotplug works, Steam will notice new pads.
  1 device reported  -> enumeration at startup only. Then the pad has to exist
                        before the game starts, or a uevent path into the
                        container is needed (fake-udev).
EOF
