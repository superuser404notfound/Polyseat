#!/usr/bin/env bash
# Kill gate three: put the seat's output into HDR and see whether it stays there.
#
# Three things have to be true at once and sway checks them in this order, in
# output_supports_hdr(): BT.2020 primaries on the output, the PQ transfer
# function on the output, and a renderer that can apply an output colour
# transform. 10-vulkan.sh supplied the third, 20-wlroots.sh the first two.
#
# sway answers both halves of the question over IPC, which is better than
# reading its log for it: get_outputs carries features.hdr, which is
# output_supports_hdr() itself, and hdr, which is whether it is switched on. So
# the two failures are distinguishable rather than both being "it did not work".
#
# Applied with swaymsg rather than written into the sway config, because the
# config is a file polyseatd regenerates on every provisioning run. Where this
# ends up living in Polyseat is a question for after the spike, not during it.
set -uo pipefail
source "$(dirname "$0")/lib.sh"

OUTPUT="${OUTPUT:-HEADLESS-1}"

# Reads one field out of get_outputs for the output being worked on.
# Prints "true", "false", or nothing at all when sway does not answer.
field() {
    as_player swaymsg -t get_outputs -r 2>/dev/null | python3 -c "
import json, sys
try:
    outputs = json.load(sys.stdin)
except Exception:
    sys.exit(0)
for o in outputs:
    if o.get('name') == '$OUTPUT':
        v = o
        for key in '$1'.split('.'):
            v = (v or {}).get(key)
        print('' if v is None else str(v).lower())
" 2>/dev/null
}

step "The output"
as_player swaymsg -t get_outputs -r 2>/dev/null | python3 -c "
import json, sys
for o in json.load(sys.stdin):
    print('   ', o.get('name'), o.get('current_mode'),
          'supports hdr:', (o.get('features') or {}).get('hdr'),
          'hdr on:', o.get('hdr'))
" 2>/dev/null || warn "swaymsg did not answer"

step "Does sway think this output can do HDR at all"
# This is output_supports_hdr() and therefore the direct answer to whether the
# patched wlroots is doing its job. False here means one of exactly three
# things, and 20-wlroots.sh and 10-vulkan.sh have already checked two of them.
supported=$(field features.hdr)
case "$supported" in
    true)
        ok "features.hdr is true, so the patched wlroots is in effect"
        ;;
    false)
        bad "features.hdr is false"
        warn "  sway says why in its log; the three possible reasons are"
        warn "    BT2020 primaries not supported by output   -> patched wlroots not loaded"
        warn "    PQ transfer function not supported         -> same"
        warn "    renderer doesn't support output color transforms -> still on gles2"
        swaylog | grep -i 'Cannot enable HDR on output' | tail -3 | sed 's/^/    /'
        exit 1
        ;;
    *)
        bad "sway did not answer, or has no output called $OUTPUT"
        exit 1
        ;;
esac

step "Turning HDR on"
# render_bit_depth is not set here on purpose. sway raises it to 10 by itself
# when HDR is enabled, and saying it twice would hide the case where the
# implicit path is the one that is broken.
out=$(as_player swaymsg -- output "$OUTPUT" hdr on 2>&1)
if grep -qi 'error\|failure\|Unknown' <<< "$out"; then
    bad "sway refused: $out"
    exit 1
fi
ok "swaymsg accepted it"

sleep 2

step "Did it take"
# swaymsg reporting success only means the command parsed. Whether the output is
# in HDR is decided later, on commit.
enabled=$(field hdr)
case "$enabled" in
    true)  ok "the output is in HDR" ;;
    false) bad "sway accepted the command and the output is still SDR"
           swaylog | grep -i hdr | tail -5 | sed 's/^/    /'
           exit 1 ;;
    *)     bad "sway stopped answering"; exit 1 ;;
esac

step "Buffer format now"
# The one measurement that says the ten bit path really took. Before HDR this
# said XRGB8888; if it still does, sway is compositing HDR into an eight bit
# buffer and everything downstream is banded even where the colours are right.
log=$(swaylog)
fmt=$(formatline "$log")
echo "    ${fmt:-no format line in this invocation of sway}"
case "$(formatdepth "$fmt")" in
    10) ok "ten bit, so HDR reached the swapchain" ;;
    8)  bad "still eight bit - sway is compositing HDR into an eight bit buffer"
        warn "  the colours may still be right and every gradient will be banded"
        exit 1 ;;
    *)  warn "could not read the format; is sway at debug level (10-vulkan.sh sets it)" ;;
esac

step "Does sway offer the protocol Sunshine has to read"
# The cheap precondition for gate four, which costs half an hour and several
# gigabytes. The patched Sunshine learns the output's colour through
# wp_color_manager_v1; if sway does not advertise that global, no amount of
# patching Sunshine can help, and it is far better to find that out here.
#
# wayland-info is the smallest client that asks. Skipped rather than installed
# behind anybody's back.
if ! insh 'command -v wayland-info >/dev/null 2>&1'; then
    warn "wayland-info not installed - skipping"
    warn "  incus exec $CT -- pacman -S --noconfirm wayland-utils"
else
    globals=$(as_player wayland-info 2>&1)
    if grep -q 'wp_color_manager_v1' <<< "$globals"; then
        ok "wp_color_manager_v1 is advertised"
        grep -A8 'wp_color_manager_v1' <<< "$globals" | sed 's/^/    /' | head -20
    elif grep -qi 'failed to connect\|Unable to connect' <<< "$globals"; then
        warn "wayland-info could not reach the compositor:"
        head -3 <<< "$globals" | sed 's/^/    /'
    else
        bad "sway does not advertise wp_color_manager_v1"
        warn "  the Sunshine patch reads the output's colour through that protocol,"
        warn "  so gate four cannot succeed and is not worth its half hour yet"
        warn "  interfaces sway does advertise:"
        grep -oE 'interface: .[a-z_0-9]+' <<< "$globals" | sort -u | sed 's/^/    /' | head -40
    fi
fi

ok "gate three passed, continue with 40-sunshine.sh"
