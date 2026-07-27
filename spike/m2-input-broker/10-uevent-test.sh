#!/usr/bin/env bash
# Die Frage, die über den ganzen Broker-Entwurf entscheidet:
# Erreicht ein per `incus config device add` eingehängtes Gerät den udev
# des Containers als Ereignis?
#
# Wenn ja, sehen libinput (also sway) und SDL das Gerät ohne jeden Kniff, und
# der Fake-udev-Shim entfällt. Wenn nein, braucht sway einen Ersatzweg.
set -uo pipefail
source "$(dirname "$0")/lib.sh"

PADNAME="polyseat:uevent-test"

cleanup() {
    incus exec "$CT" -- pkill -f 'udevadm monitor' 2>/dev/null
    pkill -f 'padgen.py --seat uevent-test' 2>/dev/null
    for d in $(incus config device list "$CT" 2>/dev/null | grep '^pad-'); do
        incus config device remove "$CT" "$d" >/dev/null 2>&1
    done
}
trap cleanup EXIT

step "udev-Monitor im Container starten"
incus exec "$CT" -- sh -c \
    'nohup udevadm monitor --udev --kernel --subsystem-match=input > /root/uevents.log 2>&1 &'
sleep 2

step "Gerät auf dem Host erzeugen"
"$HERE/../m0-input/padgen.py" --seat uevent-test --quiet > /tmp/padgen-m2.log 2>&1 &
sleep 3

node=""
for d in /sys/class/input/event*; do
    [[ -r "$d/device/name" ]] || continue
    [[ "$(<"$d/device/name")" == "$PADNAME"* ]] && node="${d##*/}"
done
[[ -n "$node" ]] || { bad "Gerät nicht am Host gefunden"; exit 1; }
ok "Host-Knoten: /dev/input/$node"

step "In den laufenden Seat einhängen"
incus config device add "$CT" "pad-$node" unix-char \
    source="/dev/input/$node" path="/dev/input/$node" required=false
sleep 4

step "Hat der Container ein Ereignis gesehen?"
events=$(incus exec "$CT" -- cat /root/uevents.log 2>/dev/null | grep -c "$node" || true)
incus exec "$CT" -- cat /root/uevents.log 2>/dev/null | tail -20

if (( events > 0 )); then
    ok "JA — $events Ereignis(se) für $node. Kein Fake-udev nötig."
else
    bad "NEIN — kein Ereignis. sway/libinput bemerkt das Gerät nicht."
fi

step "Kennt der udev des Containers das Gerät überhaupt?"
incus exec "$CT" -- udevadm info -q property -n "/dev/input/$node" 2>&1 \
    | grep -E '^(DEVNAME|ID_INPUT|MAJOR)' | head -6 \
    || warn "udevadm info kennt es nicht"

step "Sieht libinput im Container es?"
incus exec "$CT" -- sh -c 'command -v libinput >/dev/null || pacman -S --noconfirm --needed libinput >/dev/null 2>&1
    libinput list-devices 2>/dev/null | grep -A1 -i polyseat | head -6' \
    || warn "libinput listet es nicht"
