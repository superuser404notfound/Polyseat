#!/usr/bin/env bash
# Removes every trace of the spike.
set -uo pipefail
source "$(dirname "$0")/lib.sh"

step "Removing container '$CT'"
if incus info "$CT" >/dev/null 2>&1; then
    incus delete --force "$CT" && ok "deleted"
else
    ok "was not present"
fi

step "udev rule"
if [[ -e /etc/udev/rules.d/70-polyseat-hide.rules ]]; then
    echo "  The rule is still installed. Remove it (as root):"
    echo "    sudo rm /etc/udev/rules.d/70-polyseat-hide.rules && sudo udevadm control --reload"
else
    ok "no rule installed"
fi
