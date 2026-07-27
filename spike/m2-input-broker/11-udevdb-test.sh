#!/usr/bin/env bash
# Teilfrage: Wie viel fehlt dem Container — das Ereignis oder die
# udev-Datenbank?
#
# libudev baut Geräteeigenschaften nicht aus /sys, sondern aus seiner
# Datenbank unter /run/udev/data/. Die entsteht im Container nie, weil dort
# nie ein uevent ankommt. Wenn das Kopieren des Datenbankeintrags vom Host
# genügt, damit libinput das Gerät sieht, zerfällt das Problem in zwei
# unabhängige Hälften — und nur die Hotplug-Benachrichtigung braucht dann
# noch echten Netlink-Aufwand.
#
# Der Gerätename ist bewusst NICHT "polyseat:*", damit die Ausblendregel des
# Hosts die Eigenschaften nicht wegstrippt und wir einen realistischen
# Datenbankeintrag bekommen.
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

step "Gerät auf dem Host erzeugen"
"$HERE/../m0-input/padgen.py" --name "$NAME" --quiet >/dev/null 2>&1 &
sleep 3
node=""
for d in /sys/class/input/event*; do
    [[ -r "$d/device/name" ]] || continue
    [[ "$(<"$d/device/name")" == "$NAME" ]] && node="${d##*/}"
done
[[ -n "$node" ]] || { bad "Gerät nicht gefunden"; exit 1; }
minor=$(cut -d: -f2 "/sys/class/input/$node/dev")
ok "/dev/input/$node  (c13:$minor)"

step "Datenbankeintrag des Hosts"
sudo cat "/run/udev/data/c13:$minor" | sed 's/^/  /'

step "Knoten in den Seat einhängen"
incus config device add "$CT" "pad-$node" unix-char \
    source="/dev/input/$node" path="/dev/input/$node" required=false >/dev/null
sleep 2

step "VORHER — sieht libinput im Container etwas?"
incus exec "$CT" -- sh -c "libinput list-devices 2>/dev/null | grep -c 'm2probe'" \
    | sed 's/^/  Treffer: /'

step "Datenbankeintrag in den Container kopieren"
sudo cat "/run/udev/data/c13:$minor" | \
    incus exec "$CT" -- sh -c "mkdir -p /run/udev/data && cat > /run/udev/data/c13:$minor"
ok "kopiert"

step "NACHHER — udevadm info im Container"
incus exec "$CT" -- udevadm info -q property -n "/dev/input/$node" 2>&1 \
    | grep -E '^(DEVNAME|ID_INPUT|MAJOR)' | sed 's/^/  /'

step "NACHHER — libinput im Container"
if incus exec "$CT" -- sh -c "libinput list-devices 2>/dev/null | grep -q 'm2probe'"; then
    ok "libinput sieht das Gerät — die Datenbank allein genügt für Enumeration"
    incus exec "$CT" -- sh -c "libinput list-devices 2>/dev/null | grep -A2 'm2probe'" | sed 's/^/  /'
else
    bad "libinput sieht es weiterhin nicht"
fi
