#!/usr/bin/env bash
# Runs padgen.py INSIDE THE CONTAINER. Blocks - open a second shell for
# 30-observe-host.sh. Ctrl-C stops it and removes the pad again.
set -euo pipefail
source "$(dirname "$0")/lib.sh"

step "Starting padgen inside container '$CT' (seat tag: $SEAT)"
echo "Blocking. Second shell: ./30-observe-host.sh"
echo
exec incus exec "$CT" -- python3 /root/padgen.py --seat "$SEAT"
