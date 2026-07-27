#!/usr/bin/env bash
# Entfernt den Seat-Container restlos.
set -uo pipefail
source "$(dirname "$0")/lib.sh"

step "Container '$CT' entfernen"
if incus info "$CT" >/dev/null 2>&1; then
    incus delete --force "$CT" && ok "gelöscht"
else
    ok "war nicht vorhanden"
fi
