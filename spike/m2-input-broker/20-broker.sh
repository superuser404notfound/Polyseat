#!/usr/bin/env bash
# Starts the broker prototype for one seat.
#
# It has to run WHILE streaming: Sunshine creates its virtual devices when a
# client connects, and those are exactly what the broker pulls into the seat.
# If it is not running, the session stays without keyboard and mouse.
set -uo pipefail
source "$(dirname "$0")/lib.sh"

step "Copying fakeudev into the seat"
incus file push "$HERE/fakeudev.py" "$CT/root/fakeudev.py" --mode 0755 >/dev/null
ok "ready"

step "Starting the broker"
echo "  seat: $CT. Ctrl-C stops it."
echo
exec "$HERE/broker.py" --seat "$CT" "$@"
