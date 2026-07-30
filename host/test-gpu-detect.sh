#!/usr/bin/env bash
# Checks the card detection in install.sh against machines this one is not.
#
# This machine has one NVIDIA card and cannot grow a second one, so every
# interesting case for the AMD work is a machine nobody here has: an AMD card,
# two cards at once, a card with no driver bound to it. The answers are built
# out of directories instead.
#
# It runs the detection out of install.sh rather than a copy of it. A copy
# would pass forever after the original changed, which is the failure mode this
# project has already paid for once.
#
#   ./host/test-gpu-detect.sh
set -uo pipefail

HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
INSTALL="$HERE/install.sh"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# The block between the two headings, with the reporting helpers in front of it
# so it can run outside the installer.
{
    echo 'ok() { :; }; bad() { :; }; warn() { echo "WARN $*"; }; step() { :; }'
    sed -n '/^step "Graphics"$/,/^step "Prerequisites"$/p' "$INSTALL" | sed '$d'
    echo 'echo "vendor=$gpu_vendor node=$gpu_node driver=$gpu_driver"'
} > "$work/detect.sh"

grep -q 'renderD' "$work/detect.sh" || {
    echo "the detection block could not be cut out of install.sh, so this tests nothing"
    exit 1
}

# card builds one device the way sysfs really lays it out.
#
# The real thing keeps devices under /sys/devices and fills /sys/bus and
# /sys/class with symlinks into it, and every one of those links is relative.
# That detail is the whole point of building it this way: the first version of
# this file put the devices directly under bus/pci/devices, where a "driver"
# link copied from a real machine resolves to nothing, and the tests reported a
# card with no driver bound while looking at one that had.
#
#   card <root> <pci> <vendor> <driver> <class> [node...]
card() {
    local root=$1 pci=$2 vendor=$3 driver=$4 class=$5
    shift 5

    local dir="$root/devices/pci0000:00/$pci"
    mkdir -p "$dir" "$root/bus/pci/devices" "$root/class/drm"
    echo "$vendor" > "$dir/vendor"
    echo "$class" > "$dir/class"

    ln -sfn "../../../devices/pci0000:00/$pci" "$root/bus/pci/devices/$pci"

    if [[ -n $driver ]]; then
        mkdir -p "$root/bus/pci/drivers/$driver"
        ln -sfn "../../../bus/pci/drivers/$driver" "$dir/driver"
    fi

    for node in "$@"; do
        mkdir -p "$dir/drm/$node"
        ln -sfn "../../../$pci" "$dir/drm/$node/device"
        ln -sfn "../../devices/pci0000:00/$pci/drm/$node" "$root/class/drm/$node"
    done
}

fails=0

check() {
    local name=$1 root=$2 want=$3
    local got

    got=$(POLYSEAT_SYSFS="$root" bash "$work/detect.sh" 2>&1 | grep '^vendor=')

    if [[ $got == "$want" ]]; then
        printf '  \033[32m✓\033[0m %s\n' "$name"
    else
        printf '  \033[31m✗\033[0m %s\n     got  %s\n     want %s\n' "$name" "$got" "$want"
        fails=1
    fi
}

echo
echo "card detection"

root=$(mktemp -d "$work/XXXX")
card "$root" 0000:03:00.0 0x1002 amdgpu 0x030000 card0 renderD128
check "an AMD card on its own" "$root" \
    "vendor=amd node=/dev/dri/renderD128 driver=amdgpu"

root=$(mktemp -d "$work/XXXX")
card "$root" 0000:01:00.0 0x10de nvidia 0x030000 card0 renderD128
check "an NVIDIA card on its own" "$root" \
    "vendor=nvidia node=/dev/dri/renderD128 driver=nvidia"

# The machine somebody testing the AMD path will actually have: this one with a
# second card in it. The two detections have to agree, so this is the same rule
# the daemon follows in internal/seat/gpu.go.
root=$(mktemp -d "$work/XXXX")
card "$root" 0000:01:00.0 0x10de nvidia 0x030000 card0 renderD128
card "$root" 0000:03:00.0 0x1002 amdgpu 0x030000 card1 renderD129
check "both, NVIDIA wins" "$root" \
    "vendor=nvidia node=/dev/dri/renderD128 driver=nvidia"

# Order reversed, because a glob returns what the directory holds and the point
# of sorting is that the answer does not depend on it.
root=$(mktemp -d "$work/XXXX")
card "$root" 0000:03:00.0 0x1002 amdgpu 0x030000 card0 renderD128
card "$root" 0000:01:00.0 0x10de nvidia 0x030000 card1 renderD129
check "both the other way round, still NVIDIA" "$root" \
    "vendor=nvidia node=/dev/dri/renderD129 driver=nvidia"

# The case the whole PCI fallback exists for. No driver means no render node,
# and that is exactly the machine that needs to be told which driver to install
# rather than that it has no graphics card.
root=$(mktemp -d "$work/XXXX")
card "$root" 0000:03:00.0 0x1002 "" 0x030000
check "an AMD card with no driver bound" "$root" "vendor=amd node= driver="

root=$(mktemp -d "$work/XXXX")
card "$root" 0000:01:00.0 0x10de "" 0x030000
check "an NVIDIA card with no driver bound" "$root" "vendor=nvidia node= driver="

# A card with a render node beats one without, whichever way round they appear:
# a live card is always the better answer than a dormant one.
root=$(mktemp -d "$work/XXXX")
card "$root" 0000:03:00.0 0x1002 amdgpu 0x030000 card0 renderD128
card "$root" 0000:04:00.0 0x1002 "" 0x030000
check "one AMD card bound, one not" "$root" \
    "vendor=amd node=/dev/dri/renderD128 driver=amdgpu"

root=$(mktemp -d "$work/XXXX")
card "$root" 0000:00:02.0 0x8086 i915 0x030000 card0 renderD128
check "an Intel card, which nothing here can build for" "$root" \
    "vendor= node= driver="

# 0x1002 on something that is not a display controller. Without the class test
# this would be reported as a graphics card.
root=$(mktemp -d "$work/XXXX")
card "$root" 0000:03:00.1 0x1002 snd_hda_intel 0x040300
check "an AMD device that is not a GPU" "$root" "vendor= node= driver="

root=$(mktemp -d "$work/XXXX")
mkdir -p "$root/class/drm" "$root/bus/pci/devices"
check "a machine with no graphics card at all" "$root" "vendor= node= driver="

echo

if ((fails)); then
    echo "  something is wrong with the detection"
    exit 1
fi

echo "  all of it agrees with what those machines would report"
