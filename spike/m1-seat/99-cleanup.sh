#!/usr/bin/env bash
# Removes the seat container entirely.
set -uo pipefail
source "$(dirname "$0")/lib.sh"

step "Removing container '$CT'"
if incus info "$CT" >/dev/null 2>&1; then
    incus delete --force "$CT" && ok "deleted"
else
    ok "was not present"
fi
