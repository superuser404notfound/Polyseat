#!/usr/bin/env bash
# Repariert, was `nvidia.runtime=true` NICHT mitliefert.
#
# libnvidia-container spiegelt die Treiber-Bibliotheken in den Container —
# aber weder die glvnd-Manifeste, die EGL überhaupt erst zum NVIDIA-Vendor
# leiten, noch den GBM-Backend-Symlink, noch die EGL-Plattform-Bibliotheken
# (die auf Arch aus eigenen Paketen kommen und gar kein Treiberbestandteil
# sind). Ohne all das landet EGL bei Mesa, und Sunshine faellt auf
# Software-Encoding zurueck: "Found H.264 encoder: libx264 [software]".
#
# Reihenfolge beachten: erst die Pakete, dann die Manifeste. Die Pakete
# bringen ihre eigenen JSONs mit — wer sie vorher von Hand hineinkopiert,
# blockiert die Installation mit einem Dateikonflikt.
set -euo pipefail
source "$(dirname "$0")/lib.sh"

step "EGL-Plattform-Pakete (kein Treiberbestandteil, daher nicht injiziert)"
# egl-gbm liefert libnvidia-egl-gbm + 15_nvidia_gbm.json — ohne das schlägt
# Sunshines Capture mit "Couldn't initialize EGL display" fehl.
incus exec "$CT" -- pacman -S --noconfirm --needed egl-gbm egl-wayland egl-x11

step "GBM-Backend verlinken"
# Auf dem Host ist /usr/lib/gbm/nvidia-drm_gbm.so nur ein Symlink auf
# libnvidia-allocator. Die Bibliothek wird injiziert, der Symlink nicht.
incus exec "$CT" -- ln -sf ../libnvidia-allocator.so.1 /usr/lib/gbm/nvidia-drm_gbm.so

step "glvnd- und Vulkan-Manifest"
# Diese beiden gehören auf dem Host zu nvidia-utils. Das Paket im Container
# zu installieren wäre falsch — es würde eigene .so-Dateien gegen die
# injizierten setzen. Die Manifeste sind stabil und werden daher erzeugt.
incus exec "$CT" -- mkdir -p /usr/share/glvnd/egl_vendor.d /usr/share/vulkan/icd.d
incus exec "$CT" -- sh -c 'cat > /usr/share/glvnd/egl_vendor.d/10_nvidia.json <<EOF
{
    "file_format_version" : "1.0.0",
    "ICD" : {
        "library_path" : "libEGL_nvidia.so.0"
    }
}
EOF'
incus file push /usr/share/vulkan/icd.d/nvidia_icd.json \
    "$CT/usr/share/vulkan/icd.d/nvidia_icd.json"

step "GPU-Knoten für den Spieler zugänglich machen"
# /dev/dri/card1 kommt sonst als root:root 0660 an.
incus config device set "$CT" gpu mode=0666

step "Prüfen"
incus exec "$CT" -- sh -c 'ls /usr/lib/libnvidia-egl-gbm.so.1 >/dev/null' \
    && ok "egl-gbm vorhanden" || bad "egl-gbm fehlt"
incus exec "$CT" -- sh -c 'test -e /usr/lib/gbm/nvidia-drm_gbm.so' \
    && ok "GBM-Backend verlinkt" || bad "GBM-Backend fehlt"
incus exec "$CT" -- sudo -u player env XDG_RUNTIME_DIR=/run/user/1000 \
    eglinfo -B 2>/dev/null | grep -q 'EGL vendor string: NVIDIA' \
    && ok "EGL meldet NVIDIA" || bad "EGL landet nicht bei NVIDIA"
