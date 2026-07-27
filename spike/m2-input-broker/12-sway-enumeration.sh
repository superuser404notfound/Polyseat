#!/usr/bin/env bash
# Fragt den echten Konsumenten: Sieht sway ein eingehängtes Gerät, wenn es
# beim Start bereits da ist? Und trägt der kopierte udev-Datenbankeintrag
# dazu etwas bei?
#
# Zwei Durchläufe, damit die beiden Faktoren getrennt bewertbar sind:
#   A) nur der Geräteknoten
#   B) Knoten + Datenbankeintrag vom Host
#
# ACHTUNG: startet die sway-Session neu, ein laufender Moonlight-Stream
# bricht dabei ab.
set -uo pipefail
source "$(dirname "$0")/lib.sh"

NAME="m2probe Virtual Gamepad"
PLAYER=player

as_player() {
    incus exec "$CT" -- sudo -u "$PLAYER" env \
        XDG_RUNTIME_DIR=/run/user/1000 \
        DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus \
        SWAYSOCK="$(incus exec "$CT" -- sh -c \
            'ls /run/user/1000/sway-ipc.*.sock 2>/dev/null | head -1')" "$@"
}

cleanup() {
    pkill -f 'padgen.py' 2>/dev/null
    for d in $(incus config device list "$CT" 2>/dev/null | grep '^pad-'); do
        incus config device remove "$CT" "$d" >/dev/null 2>&1
    done
}
trap cleanup EXIT

count_inputs() {
    as_player swaymsg -t get_inputs 2>/dev/null | grep -c '"identifier"' || echo 0
}
show_inputs() {
    as_player swaymsg -t get_inputs 2>/dev/null \
        | grep -E '"identifier"|"name"' | head -10 | sed 's/^/    /'
}

step "Ausgangslage: Eingabegeräte in sway"
echo "  Anzahl: $(count_inputs)"

step "Gerät auf dem Host erzeugen und einhängen"
"$HERE/../m0-input/padgen.py" --name "$NAME" --quiet >/dev/null 2>&1 &
sleep 3
node=""
for d in /sys/class/input/event*; do
    [[ -r "$d/device/name" ]] || continue
    [[ "$(<"$d/device/name")" == "$NAME" ]] && node="${d##*/}"
done
[[ -n "$node" ]] || { bad "Gerät nicht gefunden"; exit 1; }
minor=$(cut -d: -f2 "/sys/class/input/$node/dev")
ok "/dev/input/$node (c13:$minor)"
incus config device add "$CT" "pad-$node" unix-char \
    source="/dev/input/$node" path="/dev/input/$node" required=false >/dev/null

step "Durchlauf A — nur der Knoten, sway neu starten"
incus exec "$CT" -- rm -f "/run/udev/data/c13:$minor"
as_player systemctl --user restart polyseat-sway.service
sleep 10
a=$(count_inputs)
echo "  Eingabegeräte: $a"
show_inputs

step "Durchlauf B — zusätzlich der udev-Datenbankeintrag"
sudo cat "/run/udev/data/c13:$minor" | \
    incus exec "$CT" -- sh -c "mkdir -p /run/udev/data && cat > /run/udev/data/c13:$minor"
as_player systemctl --user restart polyseat-sway.service
sleep 10
b=$(count_inputs)
echo "  Eingabegeräte: $b"
show_inputs

step "Auswertung"
if   (( a > 0 )); then ok  "sway sieht das Gerät schon ohne Datenbankeintrag."
elif (( b > 0 )); then ok  "sway sieht es NUR mit Datenbankeintrag — der Broker muss ihn schreiben."
else                   bad "sway sieht es in keinem Fall. libinput braucht mehr als /sys + Knoten."
fi
