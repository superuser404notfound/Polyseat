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
    prime)
        # Show the keyboard once at session start and put it away again.
        #
        # This looks pointless and is not. squeekboard registers a virtual
        # keyboard with the compositor as soon as it starts, but only gives it a
        # layout the first time it is actually shown. Until then sway hands
        # every client that asks for the keymap a zero length one, and MangoHud
        # maps it without checking the length and dereferences the failure:
        # every Vulkan application in the seat dies with SIGSEGV before it draws
        # a frame, and opening the keyboard during a game killed the game.
        #
        # Showing it once fixes it for the life of the session, so it is done
        # here, before anybody has connected and while there is nobody to see
        # it, rather than being discovered later by somebody whose game just
        # vanished. The framerate cap depends on MangoHud, so this is not
        # optional: see polyseat-fps.
        i=0
        while [ $i -lt 40 ]; do
            [ -n "$(visible)" ] && break
            i=$((i + 1))
            sleep 0.25
        done

        show true
        sleep 1
        show false
        ;;
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
