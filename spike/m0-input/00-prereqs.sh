#!/usr/bin/env bash
# Checks every prerequisite for M0. Changes nothing, only prints.
set -uo pipefail
source "$(dirname "$0")/lib.sh"

missing_pkgs=()
need_pkg() {
    if pacman -Qq "$1" >/dev/null 2>&1; then ok "package $1"
    else bad "package $1 missing"; missing_pkgs+=("$1"); fi
}

step "Packages"
need_pkg incus
need_pkg nvidia-container-toolkit
need_pkg evtest
need_pkg python

step "Incus service"
if systemctl is-active --quiet incus.socket || systemctl is-active --quiet incus; then
    ok "incus is running"
else
    bad "incus service is not running"
fi

step "Group membership"
if id -nG | tr ' ' '\n' | grep -qx incus-admin; then
    ok "$USER is in incus-admin"
else
    bad "$USER is not in incus-admin (log in again after usermod!)"
fi

step "Incus reachable"
if incus list >/dev/null 2>&1; then
    ok "incus list works"
    if incus storage list -f csv 2>/dev/null | grep -q .; then
        ok "storage pool present"
    else
        bad "no storage pool - 'incus admin init' is missing"
    fi
else
    bad "incus not reachable (service? group? admin init?)"
fi

step "Host basics"
[[ -e /dev/uinput ]] && ok "/dev/uinput present" || bad "/dev/uinput missing (modprobe uinput)"
[[ -e /dev/nvidiactl ]] && ok "NVIDIA devices present" || bad "no /dev/nvidia* devices"
grep -qs . /etc/subuid && ok "subuid configured" || warn "/etc/subuid is empty"

if ((${#missing_pkgs[@]})); then
    step "Install missing packages"
    echo "  sudo pacman -S --needed ${missing_pkgs[*]}"
fi

cat <<'EOF'

One-time root setup, if anything above is red:

  sudo pacman -S --needed incus nvidia-container-toolkit go
  sudo systemctl enable --now incus.socket
  sudo usermod -aG incus-admin "$USER"     # log in again afterwards!
  sudo incus admin init --minimal
EOF
