#!/usr/bin/env bash
# The last mile: something in the seat that actually renders HDR, streamed out.
#
# Everything before this proved the pipe. 70-hdr-layer.sh proved a surface in the
# seat offers VK_COLOR_SPACE_HDR10_ST2084_EXT to whoever asks. Nothing has asked:
# vkcube wants sRGB and always will.
#
# So this brings a client that does ask. mpv with vo=gpu-next on Vulkan picks an
# HDR10 swapchain when the content is HDR and the surface offers one, which is
# exactly the two halves that have been established separately and never
# together.
#
# The content is generated rather than downloaded. A file made here is a known
# quantity: PQ transfer, BT.2020 primaries, ten bit, with mastering display
# metadata whose red primary is 35400/50000 - the same number the Sunshine
# patch's conversion was checked against, which is a pleasing coincidence and
# nothing more.
#
# What this cannot decide is whether the picture is right. That is a person
# looking at an HDR display while the stream runs.
set -uo pipefail
source "$(dirname "$0")/lib.sh"

CLIP=/home/$PLAYER/m8-pq-test.mkv
LOG=/tmp/m8-mpv.log
DIRS=/usr/local/share:/usr/share

step "Is the layer in place"
if insh "test -f $PREFIX/share/vulkan/implicit_layer.d/VkLayer_hdr_wsi.x86_64.json"; then
    ok "the Vulkan HDR layer is installed"
else
    bad "no HDR layer - run 70-hdr-layer.sh first"
    exit 1
fi

step "A player and an encoder"
inseat pacman -S --needed --noconfirm mpv ffmpeg || { bad "pacman failed"; exit 1; }
ok "mpv and ffmpeg in place"

step "Making the clip"
# A linear ramp encoded as PQ. PQ is steeply non-linear, so the bright end of
# this is genuinely bright - on an SDR display it simply clips, which is the
# point: the difference is meant to be visible rather than subtle.
#
# The tags have to go into the bitstream through x265-params. Setting only
# ffmpeg's -color_trc and -color_primaries leaves the file reading back as
# "unknown", which was measured rather than assumed.
insh "rm -f $CLIP"
inseat sudo -u "$PLAYER" ffmpeg -hide_banner -loglevel error \
    -f lavfi -i "gradients=s=1920x1080:type=linear:d=30:r=60" \
    -vf format=yuv420p10le \
    -c:v libx265 \
    -x265-params "log-level=none:colorprim=bt2020:transfer=smpte2084:colormatrix=bt2020nc:hdr10=1:master-display=G(8500,39850)B(6550,2300)R(35400,14600)WP(15635,16450)L(10000000,1):max-cll=1000,400" \
    -y "$CLIP" || { bad "ffmpeg failed"; exit 1; }

tagged=$(inseat sudo -u "$PLAYER" ffprobe -hide_banner -loglevel error -show_streams "$CLIP" 2>/dev/null \
         | grep -E '^(color_transfer|color_primaries|pix_fmt)=')
echo "$tagged" | sed 's/^/    /'
if grep -q 'color_transfer=smpte2084' <<< "$tagged"; then
    ok "the clip is PQ, BT.2020, ten bit"
else
    bad "the clip did not come out tagged as HDR"
    exit 1
fi

step "Playing it"
# --target-colorspace-hint is the switch: it tells libplacebo to ask the surface
# for a colourspace that matches the content instead of tone-mapping down to
# whatever the swapchain came up as.
insh "rm -f $LOG"
as_player sh -c "cd /home/$PLAYER && setsid nohup env \
    XDG_DATA_DIRS='$DIRS' ENABLE_HDR_WSI=1 \
    mpv --vo=gpu-next --gpu-api=vulkan --target-colorspace-hint=yes \
        --fs --loop-file=inf --no-audio --msg-level=vo=v \
        '$CLIP' > $LOG 2>&1 < /dev/null &" \
    || { bad "could not start mpv"; exit 1; }

sleep 8

step "Did the client take an HDR swapchain"
# The decisive line, and it comes from the layer rather than from mpv, so it
# says what was actually negotiated rather than what was wanted.
sw=$(insh "grep 'HDR Layer' $LOG 2>/dev/null | grep -i 'swapchain' | tail -2")
if [ -n "$sw" ]; then
    echo "$sw" | sed 's/^/    /'
fi

if grep -qi 'HDR10_ST2084' <<< "$sw"; then
    ok "mpv is rendering into an HDR10 ST2084 swapchain"
elif [ -n "$sw" ]; then
    bad "the layer is loaded but the swapchain is not HDR10"
    warn "  mpv fell back; its own reasoning is in $LOG at vo=v"
    insh "grep -iE 'colorspace|hdr|target' $LOG | tail -10" | sed 's/^/    /'
else
    bad "no swapchain line from the layer at all"
    insh "tail -15 $LOG 2>/dev/null" | sed 's/^/    /'
fi

step "What sway sees"
as_player swaymsg -t get_tree 2>/dev/null | grep -iE '"app_id"|"name"' | grep -i mpv | head -3 | sed 's/^/    /'

step "Now look at it"
warn "connect Moonlight with HDR on, pick Desktop, and watch the ramp."
warn "on an HDR display the bright end should be far brighter than white;"
warn "if it looks like an ordinary grey gradient, the content is being"
warn "tone-mapped somewhere and the log above says by whom."
warn ""
warn "while it streams:  CT=$CT ./50-verify.sh"
warn "to stop it:        incus exec $CT -- pkill -u $PLAYER mpv"
