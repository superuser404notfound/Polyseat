#!/usr/bin/env bash
# Prüft H2, H3 und H4 — während padgen im Container läuft.
set -uo pipefail
source "$(dirname "$0")/lib.sh"

step "H2 — taucht das im Container erzeugte Pad auf dem HOST auf?"
found=""
for d in /sys/class/input/event*; do
    [[ -r "$d/device/name" ]] || continue
    name="$(<"$d/device/name")"
    if [[ "$name" == "$PAD_NAME_PREFIX"* ]]; then
        found="/dev/input/${d##*/}"
        ok "gefunden: $found  →  '$name'"
    fi
done

if [[ -z "$found" ]]; then
    bad "H2 rot — kein Gerät mit Präfix '$PAD_NAME_PREFIX' am Host"
    echo "     Läuft 20-run-pad.sh? Wurde /dev/uinput durchgereicht?"
    exit 1
fi
ok "H2 grün — uinput ist nicht namespaced, wie erwartet"

step "H3 — bleibt es im /dev/input des Containers unsichtbar?"
if incus exec "$CT" -- ls /dev/input >/dev/null 2>&1; then
    inside="$(incus exec "$CT" -- ls /dev/input 2>/dev/null)"
    if [[ -z "$inside" ]]; then
        ok "H3 grün — /dev/input im Container ist leer"
    else
        warn "H3 fraglich — Container sieht bereits: $inside"
    fi
else
    ok "H3 grün — /dev/input existiert im Container gar nicht"
fi

step "H4 — udev-Eigenschaften am Host (Basis für die Ausblend-Regel)"
udevadm info -q property -n "$found" | grep -E '^(ID_INPUT|LIBINPUT|DEVNAME|ID_SERIAL)' \
    || warn "keine ID_INPUT-Eigenschaften gesetzt"

echo
echo "Gefundener Knoten: $found"
echo "Weiter mit: ./40-inject.sh ${found##*/}"
