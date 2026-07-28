#!/usr/bin/env bash
# Checks H2, H3 and H4 - while padgen is running inside the container.
set -uo pipefail
source "$(dirname "$0")/lib.sh"

step "H2 - does the pad created inside the container show up on the HOST?"
found=""
for d in /sys/class/input/event*; do
    [[ -r "$d/device/name" ]] || continue
    name="$(<"$d/device/name")"
    if [[ "$name" == "$PAD_NAME_PREFIX"* ]]; then
        found="/dev/input/${d##*/}"
        ok "found: $found  →  '$name'"
    fi
done

if [[ -z "$found" ]]; then
    bad "H2 red - no device with prefix '$PAD_NAME_PREFIX' on the host"
    echo "     Is 20-run-pad.sh running? Was /dev/uinput passed through?"
    exit 1
fi
ok "H2 green - uinput is not namespaced, as expected"

step "H3 - does it stay invisible in the container's /dev/input?"
if incus exec "$CT" -- ls /dev/input >/dev/null 2>&1; then
    inside="$(incus exec "$CT" -- ls /dev/input 2>/dev/null)"
    if [[ -z "$inside" ]]; then
        ok "H3 green - /dev/input inside the container is empty"
    else
        warn "H3 questionable - container already sees: $inside"
    fi
else
    ok "H3 green - /dev/input does not exist in the container at all"
fi

step "H4 - udev properties on the host (basis for the hide rule)"
udevadm info -q property -n "$found" | grep -E '^(ID_INPUT|LIBINPUT|DEVNAME|ID_SERIAL)' \
    || warn "no ID_INPUT properties set"

echo
echo "Node found: $found"
echo "Next: ./40-inject.sh ${found##*/}"
