#!/usr/bin/env bash
# Put the seat back the way polyseatd left it.
#
# Everything this spike wrote is either a systemd drop-in the daemon does not
# write or a file under /usr/local, which is why it can all be taken out again
# by deleting files. Nothing was installed over a package, and no packaged file
# was edited.
#
# The build dependencies are left alone on purpose: pacman cannot reliably tell
# which of them the seat wanted anyway, and removing several gigabytes of CUDA
# from a seat somebody may want to build in again tomorrow is not a cleanup, it
# is a second long download.
set -uo pipefail
source "$(dirname "$0")/lib.sh"

step "Removing the drop-ins"
for f in \
    /home/$PLAYER/.config/systemd/user/polyseat-sway.service.d/50-hdr.conf \
    /home/$PLAYER/.config/systemd/user/polyseat-sway.service.d/60-wlroots.conf \
    /home/$PLAYER/.config/systemd/user/polyseat-sunshine.service.d/50-hdr.conf
do
    if insh "test -f $f"; then
        insh "rm -f $f" && ok "removed $f"
    else
        ok "$f was not there"
    fi
done

step "Removing what was built"
insh "rm -f $PREFIX/bin/polyseat-m8-sunshine-run" && ok "the spike's Sunshine runner"
insh "rm -f $PREFIX/bin/sway" && ok "the capability-free sway copy"
# The HDR layer is implicit, so leaving its manifest behind would arm it for
# anything in the seat that sets ENABLE_HDR_WSI, long after the spike is gone.
insh "rm -f $PREFIX/share/vulkan/implicit_layer.d/VkLayer_hdr_wsi.*.json $PREFIX/lib/libVkLayer_hdr_wsi.so" \
    && ok "the Vulkan HDR layer"
insh "rm -f /home/$PLAYER/m8-pq-test.mkv" && ok "the generated test clip"
insh "rm -rf $PREFIX/lib/libwlroots-*.so* $PREFIX/include/wlr $PREFIX/lib/pkgconfig/wlroots-*.pc" \
    && ok "the patched wlroots"
insh "rm -f $PREFIX/bin/sunshine && rm -rf $PREFIX/share/sunshine" \
    && ok "the patched Sunshine"

step "Sources"
if insh "test -d /root/m8"; then
    echo "    /root/m8 still holds the two checkouts:"
    insh "du -sh /root/m8 2>/dev/null" | sed 's/^/    /'
    echo "    remove them with:  incus exec $CT -- rm -rf /root/m8"
else
    ok "no sources left"
fi

step "Restarting the session"
as_player systemctl --user daemon-reload
as_player systemctl --user restart polyseat-sway.service
if wait_active polyseat-sway.service && wait_active polyseat-sunshine.service; then
    ok "the seat is back on the packaged stack"
else
    bad "the session did not come back - look at it before streaming"
    exit 1
fi

step "Proof"
pid=$(insh "pgrep -u $PLAYER -x sway | head -1")
[ -n "$pid" ] && insh "grep -o '/[^ ]*libwlroots[^ ]*' /proc/$pid/maps | sort -u" | sed 's/^/    /'
exe=$(insh "pgrep -u $PLAYER -x sunshine | head -1")
[ -n "$exe" ] && insh "readlink -f /proc/$exe/exe" | sed 's/^/    /'
