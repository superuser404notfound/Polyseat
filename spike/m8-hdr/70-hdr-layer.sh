#!/usr/bin/env bash
# The last open question: HDR content, without gamescope.
#
# 60-gamescope.sh established that gamescope will not offer HDR to what it runs,
# because it demands six colour management features and sway advertises two. That
# is gamescope's own bar. The two other implementations that put HDR into a
# Vulkan swapchain are far lower:
#
#   Mesa's Vulkan WSI    speaks wp-color-management-v1 itself and requires no
#                        feature list at all. Useless here: this seat is NVIDIA
#                        and the driver brings its own WSI.
#
#   VK_hdr_layer         a Vulkan *layer*, so it sits above whichever driver is
#                        loaded, NVIDIA included. Its only hard requirement is
#                        `parametric`; its two HDR10 entries carry
#                        `.extended_volume = false`, so the feature sway is
#                        missing is explicitly not needed.
#
# Every condition the layer sets is already met here: sway advertises parametric
# and st2084_pq, and wlr_color_manager_v1_primaries_list_from_renderer() emits
# sRGB and BT.2020 whenever the renderer does input colour transforms, which the
# Vulkan renderer this spike switched to does.
#
# It is an implicit layer with enable_environment ENABLE_HDR_WSI=1, so it is
# found from implicit_layer.d and switched on by that variable alone.
set -uo pipefail
source "$(dirname "$0")/lib.sh"

SRC=/root/m8/vkhdr
REPO=https://github.com/Zamundaaa/VK_hdr_layer.git
MANIFEST=$PREFIX/share/vulkan/implicit_layer.d
LOG=/tmp/m8-hdrlayer.log

step "Is the output actually in HDR"
# Asked first, because the layer reads the compositor's colour and would be
# perfectly happy to report no HDR formats on an SDR output, which reads like
# the layer failing rather than the seat not being ready.
hdr=$(as_player swaymsg -t get_outputs -r 2>/dev/null | python3 -c "
import json, sys
try:
    for o in json.load(sys.stdin):
        if o.get('hdr'):
            print('yes'); break
except Exception:
    pass" 2>/dev/null)
if [ "$hdr" = yes ]; then
    ok "an output is in HDR"
else
    bad "no output is in HDR - run 30-hdr.sh first"
    exit 1
fi

step "Build dependencies"
# Small, and most of it is already there from 20-wlroots.sh.
inseat pacman -S --needed --noconfirm base-devel git meson ninja \
    vulkan-headers vulkan-icd-loader wayland wayland-protocols vulkan-tools \
    || { bad "pacman failed"; exit 1; }
ok "toolchain in place"

step "Source"
# --recurse-submodules is not optional: vkroots, the layer framework this is
# built on, is a submodule rather than a package, and meson fails late and
# confusingly without it.
insh "rm -rf $SRC && mkdir -p $(dirname $SRC)"
inseat git clone --depth 1 --recurse-submodules "$REPO" "$SRC" \
    || { bad "clone failed"; exit 1; }
insh "test -f $SRC/subprojects/vkroots/meson.build" \
    && ok "cloned, with vkroots" \
    || { bad "vkroots did not come with it"; exit 1; }

step "Building"
insh "cd $SRC && meson setup build --prefix=$PREFIX" \
    || { bad "meson setup failed"; exit 1; }
insh "cd $SRC && ninja -C build" || { bad "build failed"; exit 1; }
insh "cd $SRC && ninja -C build install" || { bad "install failed"; exit 1; }

manifest=$(insh "ls $MANIFEST/VkLayer_hdr_wsi.*.json 2>/dev/null | head -1")
if [ -n "$manifest" ]; then
    ok "installed, manifest at $manifest"
else
    bad "no layer manifest under $MANIFEST"
    exit 1
fi

step "Does the loader see it"
# The manifest lives under /usr/local/share, which the loader only searches
# through XDG_DATA_DIRS. The session unit already carries a list that includes
# it; as_player does not, so it is passed explicitly here and in the probe.
DIRS=/usr/local/share:/usr/share
if as_player env XDG_DATA_DIRS="$DIRS" vulkaninfo --summary 2>/dev/null | grep -q VK_LAYER_hdr_wsi; then
    ok "the loader lists VK_LAYER_hdr_wsi"
else
    warn "vulkaninfo does not list the layer; it may still load, the summary does not always show implicit layers"
fi

step "What the surface offers with the layer on"
# vulkaninfo creates a real Wayland surface and prints the formats it supports,
# which is exactly the question: is VK_COLOR_SPACE_HDR10_ST2084_EXT offered.
insh "rm -f $LOG"
formats=$(as_player env XDG_DATA_DIRS="$DIRS" ENABLE_HDR_WSI=1 vulkaninfo 2>&1)

if grep -q 'HDR10_ST2084' <<< "$formats"; then
    ok "VK_COLOR_SPACE_HDR10_ST2084_EXT, enum 1000104008, is offered on this surface"
    grep -B2 'HDR10_ST2084' <<< "$formats" | head -12 | sed 's/^/    /'
else
    bad "no HDR10 colourspace on the surface"
    echo "    what the layer said:"
    grep -i 'HDR Layer' <<< "$formats" | head -10 | sed 's/^/      /'
    warn "  'lacking support for parametric image descriptions' means the compositor"
    warn "  is not offering colour management at all - check 30-hdr.sh"
    warn "  no layer output at all means ENABLE_HDR_WSI did not reach the loader"
fi

step "And with a real client"
# vkcube will not pick an HDR format - it asks for sRGB - so this proves the
# layer loads and enumerates, not that anything renders in HDR. Kept because it
# is the same client 60-gamescope.sh used, so the two are comparable.
as_player sh -c "cd /home/$PLAYER && \
    setsid nohup env XDG_DATA_DIRS='$DIRS' ENABLE_HDR_WSI=1 vkcube > $LOG 2>&1 < /dev/null &" \
    || warn "could not start vkcube"
sleep 5

if insh "grep -q 'HDR Layer' $LOG 2>/dev/null"; then
    ok "the layer is active in a real client"
    insh "grep 'HDR Layer' $LOG | head -8" | sed 's/^/    /'
else
    warn "no layer output from vkcube; see $LOG in the seat"
    insh "tail -5 $LOG 2>/dev/null" | sed 's/^/    /'
fi

insh "pkill -u $PLAYER -x vkcube" >/dev/null 2>&1

step "What this does and does not show"
warn "offering HDR10 on the surface is the compositor side answered: an"
warn "application that asks for it can have it. Whether a game then looks right"
warn "is a question about the game, and about Proton, which wants"
warn "PROTON_ENABLE_WAYLAND=1 and PROTON_ENABLE_HDR=1 to use this path at all."
