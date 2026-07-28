#!/usr/bin/env bash
# Repairs what `nvidia.runtime=true` does NOT bring along.
#
# libnvidia-container mirrors the host's driver libraries into the container -
# but neither the glvnd manifests that point EGL at the NVIDIA vendor in the
# first place, nor the GBM backend symlink, nor the EGL platform libraries
# (which on Arch come from separate packages and are not part of the driver at
# all). Without all of that EGL ends up on Mesa and Sunshine silently falls back
# to software encoding: "Found H.264 encoder: libx264 [software]".
#
# Mind the order: packages first, manifests second. The packages ship their own
# JSON files - copying those in by hand beforehand blocks the installation with
# a file conflict.
set -euo pipefail
source "$(dirname "$0")/lib.sh"

step "EGL platform packages (not part of the driver, hence not injected)"
# egl-gbm provides libnvidia-egl-gbm plus 15_nvidia_gbm.json - without it
# Sunshine's capture fails with "Couldn't initialize EGL display".
incus exec "$CT" -- pacman -S --noconfirm --needed egl-gbm egl-wayland egl-x11

step "Linking the GBM backend"
# On the host /usr/lib/gbm/nvidia-drm_gbm.so is only a symlink to
# libnvidia-allocator. The library gets injected, the symlink does not.
incus exec "$CT" -- ln -sf ../libnvidia-allocator.so.1 /usr/lib/gbm/nvidia-drm_gbm.so

step "glvnd and Vulkan manifests"
# On the host these two belong to nvidia-utils. Installing that package inside
# the container would be wrong - it would put its own .so files up against the
# injected ones. The manifests are stable, so they get generated instead.
incus exec "$CT" -- mkdir -p /usr/share/glvnd/egl_vendor.d /usr/share/vulkan/icd.d
incus exec "$CT" -- sh -c 'cat > /usr/share/glvnd/egl_vendor.d/10_nvidia.json <<JSON
{
    "file_format_version" : "1.0.0",
    "ICD" : {
        "library_path" : "libEGL_nvidia.so.0"
    }
}
JSON'
incus file push /usr/share/vulkan/icd.d/nvidia_icd.json \
    "$CT/usr/share/vulkan/icd.d/nvidia_icd.json"

step "Making the GPU nodes accessible to the player"
# Otherwise /dev/dri/card1 arrives as root:root 0660.
incus config device set "$CT" gpu mode=0666

step "Checks"
incus exec "$CT" -- sh -c 'ls /usr/lib/libnvidia-egl-gbm.so.1 >/dev/null' \
    && ok "egl-gbm present" || bad "egl-gbm missing"
incus exec "$CT" -- sh -c 'test -e /usr/lib/gbm/nvidia-drm_gbm.so' \
    && ok "GBM backend linked" || bad "GBM backend missing"
incus exec "$CT" -- sudo -u player env XDG_RUNTIME_DIR=/run/user/1000 \
    eglinfo -B 2>/dev/null | grep -q 'EGL vendor string: NVIDIA' \
    && ok "EGL reports NVIDIA" || bad "EGL does not end up on NVIDIA"
