#!/usr/bin/env bash
# Prüft alle Voraussetzungen für M0. Ändert nichts, druckt nur.
set -uo pipefail
source "$(dirname "$0")/lib.sh"

missing_pkgs=()
need_pkg() {
    if pacman -Qq "$1" >/dev/null 2>&1; then ok "Paket $1"
    else bad "Paket $1 fehlt"; missing_pkgs+=("$1"); fi
}

step "Pakete"
need_pkg incus
need_pkg nvidia-container-toolkit
need_pkg evtest
need_pkg python

step "Incus-Dienst"
if systemctl is-active --quiet incus.socket || systemctl is-active --quiet incus; then
    ok "incus läuft"
else
    bad "incus-Dienst läuft nicht"
fi

step "Gruppenmitgliedschaft"
if id -nG | tr ' ' '\n' | grep -qx incus-admin; then
    ok "$USER ist in incus-admin"
else
    bad "$USER ist nicht in incus-admin (nach usermod neu einloggen!)"
fi

step "Incus erreichbar"
if incus list >/dev/null 2>&1; then
    ok "incus list funktioniert"
    if incus storage list -f csv 2>/dev/null | grep -q .; then
        ok "Storage-Pool vorhanden"
    else
        bad "kein Storage-Pool — 'incus admin init' fehlt"
    fi
else
    bad "incus nicht ansprechbar (Dienst? Gruppe? admin init?)"
fi

step "Host-Grundlagen"
[[ -e /dev/uinput ]] && ok "/dev/uinput vorhanden" || bad "/dev/uinput fehlt (modprobe uinput)"
[[ -e /dev/nvidiactl ]] && ok "NVIDIA-Devices vorhanden" || bad "keine /dev/nvidia* Devices"
grep -qs . /etc/subuid && ok "subuid konfiguriert" || warn "/etc/subuid leer"

if ((${#missing_pkgs[@]})); then
    step "Fehlende Pakete installieren"
    echo "  sudo pacman -S --needed ${missing_pkgs[*]}"
fi

cat <<'EOF'

Einmalige Root-Einrichtung, falls oben etwas rot ist:

  sudo pacman -S --needed incus nvidia-container-toolkit go
  sudo systemctl enable --now incus.socket
  sudo usermod -aG incus-admin "$USER"     # danach neu einloggen!
  sudo incus admin init --minimal
EOF
