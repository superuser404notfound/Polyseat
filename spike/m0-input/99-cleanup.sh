#!/usr/bin/env bash
# Räumt den Spike restlos ab.
set -uo pipefail
source "$(dirname "$0")/lib.sh"

step "Container '$CT' entfernen"
if incus info "$CT" >/dev/null 2>&1; then
    incus delete --force "$CT" && ok "gelöscht"
else
    ok "war nicht vorhanden"
fi

step "udev-Regel"
if [[ -e /etc/udev/rules.d/70-polyseat-hide.rules ]]; then
    echo "  Regel liegt noch. Entfernen (root):"
    echo "    sudo rm /etc/udev/rules.d/70-polyseat-hide.rules && sudo udevadm control --reload"
else
    ok "keine Regel installiert"
fi
