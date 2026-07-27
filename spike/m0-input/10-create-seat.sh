#!/usr/bin/env bash
# Legt den Test-Container an: NVIDIA durchgereicht (H1), /dev/uinput
# durchgereicht (damit das Pad im Container erzeugt werden kann), sonst nichts.
# Insbesondere KEIN /dev/input — dessen Leere ist Teil des Tests (H3).
set -euo pipefail
source "$(dirname "$0")/lib.sh"

if incus info "$CT" >/dev/null 2>&1; then
    warn "Container '$CT' existiert bereits — 99-cleanup.sh zuerst."
    exit 1
fi

step "Container '$CT' anlegen"
incus launch images:archlinux/current "$CT" \
    -c nvidia.runtime=true \
    -c nvidia.driver.capabilities=all

step "/dev/uinput durchreichen"
incus config device add "$CT" uinput unix-char \
    source=/dev/uinput path=/dev/uinput required=false

step "Warten bis Netzwerk steht"
for _ in $(seq 30); do
    incus exec "$CT" -- getent hosts geo.mirror.pkgbuild.com >/dev/null 2>&1 && break
    sleep 1
done

step "Testwerkzeug im Container installieren"
incus exec "$CT" -- pacman -Sy --noconfirm --needed \
    python evtest gcc pkgconf sdl2-compat 2>/dev/null \
 || incus exec "$CT" -- pacman -Sy --noconfirm --needed \
    python evtest gcc pkgconf sdl2

step "Spike-Dateien hineinkopieren"
incus file push "$HERE/padgen.py"  "$CT/root/padgen.py"  --mode 0755
incus file push "$HERE/sdlprobe.c" "$CT/root/sdlprobe.c"

step "H1 — sieht der Container die GPU?"
if incus exec "$CT" -- nvidia-smi -L; then
    ok "H1 grün"
else
    bad "H1 rot — nvidia.runtime greift nicht"
fi

step "Ausgangszustand /dev/input im Container"
incus exec "$CT" -- ls -l /dev/input 2>&1 || echo "  (/dev/input existiert nicht — erwartet)"

echo
echo "Weiter mit: ./20-run-pad.sh"
