#!/bin/sh
# Polyseat - show or hide the on-screen keyboard in a seat.
#
# This exists because of one case that nothing else in the chain covers.
# Signing in to a store from a client that has no keyboard: an Apple TV, a
# phone, a television. Moonlight cannot help there, it has an open request for
# exactly this and an open bug for keyboard passthrough on tvOS, so what it
# sends is modifiers rather than text. Steam's own keyboard covers Steam and,
# unreliably, whatever has been added to it as a non-Steam game. Neither
# reaches a browser window in a launcher.
#
# So the keyboard is rendered inside the seat and travels in the video stream
# like everything else, and the controller drives it as a mouse. Reachable
# three ways on purpose, because whoever needs it has the fewest input devices
# of anybody: Super+K, the bar, and a gamepad button.
#
# squeekboard was written for phones and turns out to work on sway unchanged,
# which is worth writing down because the documentation suggests otherwise.

: "${XDG_RUNTIME_DIR:=/run/user/$(id -u)}"
export XDG_RUNTIME_DIR

BUS=sm.puri.OSK0
OBJ=/sm/puri/OSK0

visible() {
    busctl --user get-property "$BUS" "$OBJ" "$BUS" Visible 2>/dev/null |
        awk '{print $2}'
}

show() {
    busctl --user call "$BUS" "$OBJ" "$BUS" SetVisible b "$1" >/dev/null 2>&1
}

case "$1" in
    show) show true ;;
    hide) show false ;;
    *)
        # Toggle. An unreadable property means squeekboard is not up yet, and
        # the useful thing to do about that is show the keyboard rather than
        # report a bus error to somebody holding a gamepad.
        if [ "$(visible)" = "true" ]; then
            show false
        else
            show true
        fi
        ;;
esac

exit 0
