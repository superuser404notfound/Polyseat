#!/usr/bin/env bash
# H5 — schiebt den Host-Knoten in den LAUFENDEN Container.
# Genau das wird der Input-Broker später automatisch tun.
set -euo pipefail
source "$(dirname "$0")/lib.sh"

node="${1:-}"
[[ -n "$node" ]] || { echo "Aufruf: $0 eventN"; exit 1; }
node="${node##*/}"
[[ -e "/dev/input/$node" ]] || { bad "/dev/input/$node existiert nicht"; exit 1; }

step "H5 — unix-char-Hotplug in den laufenden Container"
incus config device add "$CT" "pad-$node" unix-char \
    source="/dev/input/$node" path="/dev/input/$node" required=false

sleep 1
if incus exec "$CT" -- test -e "/dev/input/$node"; then
    ok "H5 grün — Knoten ist im laufenden Container angekommen"
    incus exec "$CT" -- ls -l "/dev/input/$node"
else
    bad "H5 rot — Knoten nicht im Container sichtbar"
    exit 1
fi

echo
echo "Weiter mit: ./50-verify.sh"
