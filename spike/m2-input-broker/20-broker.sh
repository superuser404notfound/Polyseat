#!/usr/bin/env bash
# Starts the broker prototype for one seat.
#
# It has to run WHILE streaming: Sunshine creates its virtual devices when a
# client connects, and those are exactly what the broker pulls into the seat.
# If it is not running, the session stays without keyboard and mouse.
#
# Needs root since the structural attribution was added: reading another
# process's file descriptors and pidfd_getfd are privileged.
set -uo pipefail
source "$(dirname "$0")/lib.sh"

step "Copying fakeudev into the seat"
incus file push "$HERE/fakeudev.py" "$CT/root/fakeudev.py" --mode 0755 >/dev/null
ok "ready"

if [[ $EUID -ne 0 ]]; then
    bad "needs root (structural attribution reads foreign descriptors)"
    echo "  sudo $0 $*"
    exit 1
fi

step "Starting the broker"
echo "  seat: $CT. Ctrl-C stops it."
echo
exec "$HERE/broker.py" --seat "$CT" "$@"
