#!/usr/bin/env bash
# Creates the test container: /dev/uinput passed through (so the pad can be
# created inside the container), NVIDIA passed through (H1), nothing else.
# In particular NO /dev/input - its emptiness is part of the test (H3).
#
# The order is not arbitrary: `nvidia.runtime=true` mirrors the host's driver
# libraries into /usr/lib inside the container. Installing a package afterwards
# that claims the same paths makes pacman abort:
#   mesa: /usr/lib/libGLX_indirect.so.0 exists in filesystem
# `--overwrite` would be the wrong answer - it would replace the injected
# driver file with mesa's. So: install first, then switch NVIDIA on. For the
# real seats this means the seat image is built without nvidia.runtime and only
# started with it at runtime.
set -euo pipefail
source "$(dirname "$0")/lib.sh"

if incus info "$CT" >/dev/null 2>&1; then
    warn "Container '$CT' already exists - run 99-cleanup.sh first."
    exit 1
fi

step "Creating container '$CT' (still without NVIDIA)"
incus launch images:archlinux/current "$CT"

step "Waiting for networking"
for _ in $(seq 60); do
    incus exec "$CT" -- getent hosts geo.mirror.pkgbuild.com >/dev/null 2>&1 && break
    sleep 1
done

step "Installing test tools"
# sdl2-compat provides the SDL2 API (real SDL2 is gone from Arch) and sits on
# top of SDL3 - so what H6 actually measures is SDL3's enumeration path.
incus exec "$CT" -- pacman -Syu --noconfirm --needed \
    python evtest gcc pkgconf sdl2-compat

step "Enabling NVIDIA and restarting"
# nvidia.runtime only mirrors the driver libraries into the container. The
# device nodes (/dev/nvidia*) require an additional gpu device - without it
# nvidia-smi runs but reports "No devices found".
incus config set "$CT" nvidia.runtime=true nvidia.driver.capabilities=all
incus config device add "$CT" gpu gpu
incus restart "$CT"
sleep 3

step "Passing through /dev/uinput"
incus config device add "$CT" uinput unix-char \
    source=/dev/uinput path=/dev/uinput required=false

step "Copying spike files in"
incus file push "$HERE/padgen.py"  "$CT/root/padgen.py"  --mode 0755
incus file push "$HERE/sdlprobe.c" "$CT/root/sdlprobe.c"

step "H1 - does the container see the GPU?"
# nvidia-smi -L exits 0 even without a GPU ("No devices found"), so check the
# output rather than the return code.
if incus exec "$CT" -- nvidia-smi -L | tee /dev/stderr | grep -q '^GPU'; then
    ok "H1 green"
else
    bad "H1 red - libraries present but no device nodes? Check the gpu device."
fi

step "Initial state of /dev/input inside the container"
incus exec "$CT" -- ls -l /dev/input 2>&1 || echo "  (/dev/input does not exist - expected)"

echo
echo "Next: ./20-run-pad.sh"
