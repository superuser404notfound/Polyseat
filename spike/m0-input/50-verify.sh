#!/usr/bin/env bash
# H6 - the actual test: does SDL inside the container recognise the pad?
set -uo pipefail
source "$(dirname "$0")/lib.sh"

step "Raw evdev access inside the container (precursor to H6)"
node="$(incus exec "$CT" -- sh -c 'ls /dev/input/event* 2>/dev/null | head -1')"
if [[ -z "$node" ]]; then
    bad "no event node inside the container - run 40-inject.sh first"
    exit 1
fi
# evtest has no --info; it prints the device description on startup and then
# enters capture mode, hence the timeout. Its exit code 124 is the normal case
# here - what gets checked is the output, not the return code.
if incus exec "$CT" -- timeout 2 evtest "$node" 2>&1 | tee /dev/stderr \
     | grep -q '^Input device name'; then
    ok "evdev readable - the node works"
else
    bad "evtest returns no device description"
fi

step "Building sdlprobe"
incus exec "$CT" -- sh -c \
    'cd /root && gcc -o sdlprobe sdlprobe.c $(pkg-config --cflags --libs sdl2)' \
    || { bad "build failed"; exit 1; }

step "H6 (a) - SDL with udev enumeration, the way a game would do it"
incus exec "$CT" -- /root/sdlprobe
rc_a=$?

step "H6 (b) - SDL with SDL_JOYSTICK_DISABLE_UDEV=1"
incus exec "$CT" --env SDL_JOYSTICK_DISABLE_UDEV=1 -- /root/sdlprobe
rc_b=$?

step "Result"
if   ((rc_a == 0)); then ok  "H6 green with no tricks - the architecture holds."
elif ((rc_b == 0)); then warn "H6 green only with SDL_JOYSTICK_DISABLE_UDEV=1."
                         echo "     It holds, but: the variable belongs in every seat's"
                         echo "     environment, and Steam Input needs checking separately."
else                     bad "H6 red - a fake-udev shim is needed, or fall back to"
                         echo "     one Unix user per seat."
fi

echo
echo "Please record the result in README.md."
