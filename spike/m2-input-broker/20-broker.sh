#!/usr/bin/env bash
# Startet den Broker-Prototyp für einen Seat.
#
# Muss laufen, WÄHREND gestreamt wird: Sunshine legt seine virtuellen Geräte
# an, wenn sich ein Client verbindet, und genau die holt der Broker in den
# Seat. Läuft er nicht, bleibt die Session ohne Tastatur und Maus.
set -uo pipefail
source "$(dirname "$0")/lib.sh"

step "fakeudev in den Seat kopieren"
incus file push "$HERE/fakeudev.py" "$CT/root/fakeudev.py" --mode 0755 >/dev/null
ok "bereit"

step "Broker starten"
echo "  Seat: $CT — Strg-C beendet."
echo
exec "$HERE/broker.py" --seat "$CT" "$@"
