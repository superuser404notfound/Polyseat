#!/usr/bin/env bash
# H7 — Hotplug zur Laufzeit.
#
# H6 zeigt nur, dass SDL beim Start findet, was schon da ist. Steam läuft im
# Seat aber dauerhaft, und Pads kommen erst dazu, wenn sich jemand verbindet.
# Ob das bemerkt wird, hängt an udev-Netlink-Uevents — und die erreichen einen
# Container normalerweise nicht.
#
# Voraussetzung: 40-inject.sh gelaufen, erstes Pad läuft.
set -uo pipefail
source "$(dirname "$0")/lib.sh"

step "sdlprobe im Beobachtungsmodus starten (30 s)"
incus exec "$CT" -- sh -c \
    'cd /root && nohup ./sdlprobe --watch 30 > /root/watch.log 2>&1 &'
sleep 4
incus exec "$CT" -- cat /root/watch.log 2>/dev/null

step "Zweites Pad im Container erzeugen"
# Eigene vendor:product, damit SDL nicht sein Xbox-Mapping darüberlegt und
# beide Pads in der Ausgabe unterscheidbar bleiben.
incus exec "$CT" -- sh -c \
    'nohup python3 -u /root/padgen.py --seat m0b --quiet --vendor 0x1234 --product 0x5678 > /root/pad-b.log 2>&1 &'
sleep 3
incus exec "$CT" -- cat /root/pad-b.log

step "Host-Knoten des zweiten Pads suchen"
node=""
for d in /sys/class/input/event*; do
    [[ -r "$d/device/name" ]] || continue
    [[ "$(<"$d/device/name")" == "polyseat:m0b"* ]] && node="${d##*/}"
done
[[ -n "$node" ]] || { bad "zweites Pad nicht am Host gefunden"; exit 1; }
ok "gefunden: /dev/input/$node"

step "Hotplug in den laufenden Container"
incus config device add "$CT" "pad-$node" unix-char \
    source="/dev/input/$node" path="/dev/input/$node" required=false

step "Warten, bis der Beobachter fertig ist"
sleep 25

step "Was hat SDL bemerkt?"
incus exec "$CT" -- cat /root/watch.log

cat <<'EOF'

Auswertung:
  2 gemeldete Geräte  -> Hotplug funktioniert, Steam bemerkt neue Pads.
  1 gemeldetes Gerät  -> nur Enumeration beim Start. Dann muss entweder das
                         Pad vor dem Spielstart da sein, oder es braucht einen
                         Uevent-Weg in den Container (fake-udev).
EOF
