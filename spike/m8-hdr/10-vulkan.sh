#!/usr/bin/env bash
# Kill gate one: does sway come up on the Vulkan renderer, headless, on this
# machine's driver, inside a container?
#
# This is first because it is the cheapest thing that can end the whole idea.
# sway's `output ... hdr` checks three things, and the third is
# `renderer->features.output_color_transform`, which the gles2 renderer does not
# have and never will. So HDR in a seat means the Vulkan renderer, and the seat
# has run on gles2 since M1 - deliberately, because that is what was measured to
# work. Nobody has ever started this session on Vulkan.
#
# Nothing here is HDR yet. If sway does not survive this step there is no point
# building anything.
set -uo pipefail
source "$(dirname "$0")/lib.sh"

DROPIN=/home/$PLAYER/.config/systemd/user/polyseat-sway.service.d/50-hdr.conf

step "Where the card is"
node=$(insh 'ls /dev/dri/renderD* 2>/dev/null | head -1')
[ -n "$node" ] && ok "render node $node" || { bad "no render node in the seat"; exit 1; }

step "Writing the drop-in"
# A file polyseatd does not write, so a provisioning run leaves it alone and
# 99-cleanup.sh can simply delete it. 50- sorts after the daemon's 20-gpu.conf,
# so this wins where the two say anything about the same variable.
insh "cat > $DROPIN <<'CONF'
# Polyseat spike M8: the renderer HDR needs.
# Written by spike/m8-hdr/10-vulkan.sh. Not written by polyseatd, and safe to
# delete: without it the session goes back to gles2 and back to SDR.
[Service]
# sway refuses to enable HDR on an output unless the renderer can apply an
# output colour transform, and gles2 cannot. This is the whole reason the seat
# has to change renderer at all.
Environment=WLR_RENDERER=vulkan
# Which card the Vulkan renderer picks. Redundant with one card in the machine
# and the whole answer with two, and a wrong pick here looks like a driver bug.
Environment=WLR_RENDER_DRM_DEVICE=$node
# sway's own log, at debug level, is the measuring instrument for the next two
# steps: it prints the buffer format it chose for the output, which is how we
# find out whether the 10 bit path actually took.
ExecStart=
ExecStart=/usr/bin/sway -d
CONF"
ok "wrote $DROPIN"

insh "chown $PLAYER:$PLAYER $DROPIN"

step "Restarting the session"
# Restarting sway takes Sunshine with it, by BindsTo. That is fine here and it
# is also the point: whatever comes back has to be a working seat, not just a
# working compositor.
as_player systemctl --user daemon-reload
as_player systemctl --user restart polyseat-sway.service

if wait_active polyseat-sway.service; then
    ok "sway is running"
else
    bad "sway did not come back - the Vulkan renderer does not work here"
    step "What sway said"
    as_player journalctl --user -u polyseat-sway.service --no-pager -n 40 | sed 's/^/    /'
    exit 1
fi

step "Which renderer it actually picked"
# Asked of the log rather than assumed from the variable: wlroots falls back to
# gles2 when the Vulkan renderer cannot be created, and it does so quietly. A
# seat that fell back would pass every check below and fail at `hdr on` with a
# message about colour transforms that points nowhere near the cause.
log=$(swaylog)
# A here-string and not `echo "$log" | grep -q`. With `set -o pipefail`,
# grep -q exits at the first match, echo dies of SIGPIPE with 141, and pipefail
# hands that 141 to the `if` - so a pattern that matches reads as one that does
# not. It only bites once the input is large enough that echo is still writing,
# which is exactly what happened when this stopped guessing a log window and
# started reading the whole invocation. Measured, not theorised: 400 lines
# passed and 200000 failed.
if grep -qi 'renderer.*vulkan\|vulkan.*renderer\|Found suitable Vulkan device\|created vulkan' <<< "$log"; then
    ok "Vulkan renderer in use"
    echo "$log" | grep -i vulkan | tail -5 | sed 's/^/    /'
else
    bad "no sign of the Vulkan renderer in sway's log"
    echo "$log" | grep -iE 'render|gles|vulkan' | tail -12 | sed 's/^/    /'
    warn "if this says gles2, the fallback happened and HDR cannot work"
    exit 1
fi

step "Buffer format the output came up with"
# Still 8 bit at this point, and it should be: nothing has asked for HDR yet.
# Printed so that the same line after 30-hdr.sh can be compared against it.
fmt=$(formatline "$log")
if [ -n "$fmt" ]; then
    echo "    $fmt"
    ok "$(formatdepth "$fmt") bit, which is what it should be before anything asks for HDR"
else
    warn "no format line in this invocation's log - 30-hdr.sh needs it, look before continuing"
fi

step "And is the seat still a seat"
if wait_active polyseat-sunshine.service; then
    ok "Sunshine came back with it"
else
    warn "Sunshine did not come back; look before continuing"
fi

ok "gate one passed, continue with 20-wlroots.sh"
