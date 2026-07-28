#!/usr/bin/env bash
# H5 - pushes the host node into the RUNNING container.
# This is exactly what the input broker will later do automatically.
set -euo pipefail
source "$(dirname "$0")/lib.sh"

node="${1:-}"
[[ -n "$node" ]] || { echo "Usage: $0 eventN"; exit 1; }
node="${node##*/}"
[[ -e "/dev/input/$node" ]] || { bad "/dev/input/$node does not exist"; exit 1; }

step "H5 - unix-char hotplug into the running container"
incus config device add "$CT" "pad-$node" unix-char \
    source="/dev/input/$node" path="/dev/input/$node" required=false

sleep 1
if incus exec "$CT" -- test -e "/dev/input/$node"; then
    ok "H5 green - the node arrived in the running container"
    incus exec "$CT" -- ls -l "/dev/input/$node"
else
    bad "H5 red - node not visible inside the container"
    exit 1
fi

echo
echo "Next: ./50-verify.sh"
