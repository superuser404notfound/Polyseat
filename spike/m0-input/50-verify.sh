#!/usr/bin/env bash
# H6 — der eigentliche Test: erkennt SDL im Container das Pad?
set -uo pipefail
source "$(dirname "$0")/lib.sh"

step "Roher evdev-Zugriff im Container (Vorstufe zu H6)"
node="$(incus exec "$CT" -- sh -c 'ls /dev/input/event* 2>/dev/null | head -1')"
if [[ -z "$node" ]]; then
    bad "kein event-Knoten im Container — 40-inject.sh zuerst"
    exit 1
fi
# evtest kennt kein --info; es druckt die Gerätebeschreibung beim Start und
# geht dann in den Capture-Modus, deshalb der timeout.
if incus exec "$CT" -- timeout 2 evtest "$node" 2>&1 | head -24; then
    ok "evdev lesbar — der Knoten funktioniert"
else
    bad "evtest schlägt fehl"
fi

step "sdlprobe bauen"
incus exec "$CT" -- sh -c \
    'cd /root && gcc -o sdlprobe sdlprobe.c $(pkg-config --cflags --libs sdl2)' \
    || { bad "Build fehlgeschlagen"; exit 1; }

step "H6 (a) — SDL mit udev-Enumeration, so wie ein Spiel es täte"
incus exec "$CT" -- /root/sdlprobe
rc_a=$?

step "H6 (b) — SDL mit SDL_JOYSTICK_DISABLE_UDEV=1"
incus exec "$CT" --env SDL_JOYSTICK_DISABLE_UDEV=1 -- /root/sdlprobe
rc_b=$?

step "Ergebnis"
if   ((rc_a == 0)); then ok  "H6 grün ohne Kniff — Architektur trägt."
elif ((rc_b == 0)); then warn "H6 grün nur mit SDL_JOYSTICK_DISABLE_UDEV=1."
                         echo "     Trägt, aber: Variable gehört in jede Seat-Umgebung,"
                         echo "     und Steam Input muss separat geprüft werden."
else                     bad "H6 rot — Fake-udev-Shim nötig oder Rückfall auf"
                         echo "     einen Unix-User pro Seat."
fi

echo
echo "Ergebnis bitte in README.md eintragen."
