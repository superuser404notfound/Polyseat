#!/usr/bin/env bash
# Legt den Test-Container an: /dev/uinput durchgereicht (damit das Pad im
# Container erzeugt werden kann), NVIDIA durchgereicht (H1), sonst nichts.
# Insbesondere KEIN /dev/input — dessen Leere ist Teil des Tests (H3).
#
# Reihenfolge ist nicht beliebig: `nvidia.runtime=true` spiegelt die
# Treiberbibliotheken des Hosts nach /usr/lib im Container. Wird danach ein
# Paket installiert, das dieselben Pfade beansprucht, bricht pacman ab:
#   mesa: /usr/lib/libGLX_indirect.so.0 exists in filesystem
# `--overwrite` wäre die falsche Antwort — es würde die injizierte
# Treiberdatei durch die von mesa ersetzen. Also: erst installieren, dann
# NVIDIA einschalten. Für die echten Seats heißt das, dass das Seat-Image
# ohne nvidia.runtime gebaut und erst zur Laufzeit damit gestartet wird.
set -euo pipefail
source "$(dirname "$0")/lib.sh"

if incus info "$CT" >/dev/null 2>&1; then
    warn "Container '$CT' existiert bereits — 99-cleanup.sh zuerst."
    exit 1
fi

step "Container '$CT' anlegen (noch ohne NVIDIA)"
incus launch images:archlinux/current "$CT"

step "Warten bis Netzwerk steht"
for _ in $(seq 60); do
    incus exec "$CT" -- getent hosts geo.mirror.pkgbuild.com >/dev/null 2>&1 && break
    sleep 1
done

step "Testwerkzeug installieren"
# sdl2-compat stellt die SDL2-API bereit (echtes SDL2 gibt es in Arch nicht
# mehr) und setzt darunter auf SDL3 auf — für H6 ist also SDL3s
# Enumerationspfad das, was gemessen wird.
incus exec "$CT" -- pacman -Syu --noconfirm --needed \
    python evtest gcc pkgconf sdl2-compat

step "NVIDIA einschalten und neu starten"
# nvidia.runtime spiegelt nur die Treiber-Bibliotheken in den Container.
# Die Geräteknoten (/dev/nvidia*) kommen erst über ein gpu-Device dazu —
# ohne das läuft nvidia-smi, meldet aber "No devices found".
incus config set "$CT" nvidia.runtime=true nvidia.driver.capabilities=all
incus config device add "$CT" gpu gpu
incus restart "$CT"
sleep 3

step "/dev/uinput durchreichen"
incus config device add "$CT" uinput unix-char \
    source=/dev/uinput path=/dev/uinput required=false

step "Spike-Dateien hineinkopieren"
incus file push "$HERE/padgen.py"  "$CT/root/padgen.py"  --mode 0755
incus file push "$HERE/sdlprobe.c" "$CT/root/sdlprobe.c"

step "H1 — sieht der Container die GPU?"
# nvidia-smi -L liefert auch ohne GPU Exit 0 ("No devices found"),
# deshalb wird die Ausgabe geprüft, nicht der Rückgabewert.
if incus exec "$CT" -- nvidia-smi -L | tee /dev/stderr | grep -q '^GPU'; then
    ok "H1 grün"
else
    bad "H1 rot — Bibliotheken da, aber keine Geräteknoten? gpu-Device prüfen."
fi

step "Ausgangszustand /dev/input im Container"
incus exec "$CT" -- ls -l /dev/input 2>&1 || echo "  (/dev/input existiert nicht — erwartet)"

echo
echo "Weiter mit: ./20-run-pad.sh"
