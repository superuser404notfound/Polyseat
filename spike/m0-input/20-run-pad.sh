#!/usr/bin/env bash
# Startet padgen.py IM CONTAINER. Blockiert — zweite Shell für 30-observe-host.sh
# öffnen. Strg-C beendet und räumt das Pad wieder ab.
set -euo pipefail
source "$(dirname "$0")/lib.sh"

step "padgen im Container '$CT' starten (Seat-Tag: $SEAT)"
echo "Blockiert. Zweite Shell: ./30-observe-host.sh"
echo
exec incus exec "$CT" -- python3 /root/padgen.py --seat "$SEAT"
