#!/usr/bin/env bash
# Kill gate two: a wlroots whose headless output will admit to BT.2020 and PQ.
#
# Everything else in the chain already exists. sway 1.12 has `output ... hdr`,
# gamescope is a full colour management client, and Sunshine's encoder path has
# handled 10 bit since KMS capture learned HDR. The one place the chain is cut
# is `output_supports_hdr()` in sway, which asks the wlroots output whether it
# supports BT.2020 primaries and the PQ transfer function - and in wlroots only
# the DRM backend ever says yes, out of the monitor's EDID.
#
# A headless output has no monitor and no EDID, so it is not that it says no for
# a reason; it is that nobody ever gave it an answer. See the patch, which is
# ten lines and argues exactly that.
#
# Built into /usr/local and picked up through LD_LIBRARY_PATH, so the packaged
# wlroots0.20 stays untouched and this is undone by deleting a drop-in.
set -uo pipefail
source "$(dirname "$0")/lib.sh"

PATCH="$HERE/patches/wlroots-headless-hdr.patch"
SRC=/root/m8/wlroots
DROPIN=/home/$PLAYER/.config/systemd/user/polyseat-sway.service.d/60-wlroots.conf

[ -f "$PATCH" ] || { bad "missing $PATCH"; exit 1; }

step "Build dependencies"
# Taken from the Arch wlroots0.20 PKGBUILD rather than guessed, so that what is
# built here is the same library the seat already had, plus the patch.
inseat pacman -S --needed --noconfirm \
    base-devel git meson ninja glslang vulkan-headers wayland-protocols \
    libdisplay-info libdrm libglvnd libinput lcms2 libliftoff pixman seatd \
    libxcb libxkbcommon xcb-util-errors xcb-util-renderutil xcb-util-wm hwdata \
    || { bad "pacman failed"; exit 1; }
ok "toolchain in place"

step "Source at $WLROOTS_TAG"
insh "rm -rf $SRC && mkdir -p $(dirname $SRC)"
inseat git clone --depth 1 --branch "$WLROOTS_TAG" \
    https://gitlab.freedesktop.org/wlroots/wlroots.git "$SRC" \
    || { bad "clone failed"; exit 1; }
ok "cloned"

step "Applying the patch"
"${INCUS[@]}" file push "$PATCH" "$CT/root/m8/wlroots-headless-hdr.patch" >/dev/null
# Dry run first, and stop on failure rather than build an unpatched library that
# would look identical and behave exactly as before.
if insh "cd $SRC && patch -p1 --dry-run < /root/m8/wlroots-headless-hdr.patch"; then
    insh "cd $SRC && patch -p1 < /root/m8/wlroots-headless-hdr.patch" >/dev/null
    ok "patched"
else
    bad "the patch does not apply to $WLROOTS_TAG"
    exit 1
fi

step "Building"
# examples off because they need more of X than a seat has, and the spike wants
# the library and nothing else.
insh "cd $SRC && meson setup build --prefix=$PREFIX -Dexamples=false" \
    || { bad "meson setup failed"; exit 1; }
insh "cd $SRC && ninja -C build" || { bad "build failed"; exit 1; }
insh "cd $SRC && ninja -C build install" || { bad "install failed"; exit 1; }
ok "installed into $PREFIX"

step "Does it carry the soname the packaged sway looks for"
want=$(insh "objdump -p /usr/bin/sway | awk '/NEEDED/ && /wlroots/ {print \$2}'")
have=$(insh "ls $PREFIX/lib/libwlroots-*.so* 2>/dev/null | xargs -r -n1 basename")
echo "    sway wants: ${want:-?}"
echo "    built:      ${have:-nothing}"
if [ -n "$want" ] && grep -qx "$want" <<< "$have"; then
    ok "the names match, sway will load this one"
else
    bad "the built library does not carry the name sway is asking for"
    warn "  a mismatched soname means sway silently keeps using the packaged library"
    exit 1
fi

step "A sway that the loader will listen to"
# LD_LIBRARY_PATH alone is not enough here, and the reason took a while to find.
#
# Arch ships sway with a file capability, cap_sys_nice, so that the compositor
# can raise its own scheduling priority. A binary with capabilities runs in what
# glibc calls secure-execution mode, and in that mode ld.so discards
# LD_LIBRARY_PATH outright. The variable is in the process environment, systemd
# put it there correctly, and the loader simply does not read it - so sway keeps
# loading the packaged /usr/lib/libwlroots-0.20.so and nothing says why.
#
# A copy of the binary is the surgical way out: file capabilities live in the
# security.capability extended attribute and plain cp does not carry those, so
# the copy is not privileged and the loader honours the variable again. Nothing
# packaged is modified, and the whole thing is undone by deleting one file.
#
# The price is stated rather than hidden: this sway cannot raise its scheduling
# priority. That is acceptable for measuring colour and would not be acceptable
# in the product, where the patched wlroots would be a rebuilt package sitting
# in /usr/lib and this whole problem would not exist.
insh "cp /usr/bin/sway $PREFIX/bin/sway && chmod 755 $PREFIX/bin/sway"
caps=$(insh "getcap $PREFIX/bin/sway 2>/dev/null")
if [ -z "$caps" ]; then
    ok "copied sway to $PREFIX/bin/sway, without capabilities"
else
    bad "the copy still carries capabilities: $caps"
    warn "  the loader will ignore LD_LIBRARY_PATH again; cp must have preserved xattrs"
    exit 1
fi

step "Pointing the session at it"
insh "cat > $DROPIN <<'CONF'
# Polyseat spike M8: the patched wlroots, and a sway that can load it.
# Written by spike/m8-hdr/20-wlroots.sh. Deleting this file puts the session
# back on the packaged wlroots0.20 and the packaged sway, and therefore back to
# SDR.
#
# The ExecStart override is not cosmetic: the packaged /usr/bin/sway carries
# cap_sys_nice, and a binary with capabilities makes ld.so discard
# LD_LIBRARY_PATH. See 20-wlroots.sh.
[Service]
Environment=LD_LIBRARY_PATH=$PREFIX/lib
ExecStart=
ExecStart=$PREFIX/bin/sway -d
CONF"
insh "chown $PLAYER:$PLAYER $DROPIN"
ok "wrote $DROPIN"

as_player systemctl --user daemon-reload
as_player systemctl --user restart polyseat-sway.service

if wait_active polyseat-sway.service; then
    ok "sway came back"
else
    bad "sway did not come back on the patched library"
    as_player journalctl --user -u polyseat-sway.service --no-pager -n 40 | sed 's/^/    /'
    exit 1
fi

step "Did the unit actually take the variable"
# Asked before looking at the process, because these are two different failures
# with one symptom. If systemd never got the drop-in there is nothing to debug
# in the loader.
eff=$(as_player systemctl --user show polyseat-sway.service -p Environment --value 2>/dev/null)
if grep -q "LD_LIBRARY_PATH=$PREFIX/lib" <<< "$eff"; then
    ok "the unit carries LD_LIBRARY_PATH=$PREFIX/lib"
else
    bad "the unit has no LD_LIBRARY_PATH"
    echo "    what it has:"
    tr ' ' '\n' <<< "$eff" | sed 's/^/      /'
    exit 1
fi

step "And is it really the patched one"
# Asked of the running process, because LD_LIBRARY_PATH is easy to lose and the
# failure is silent: sway starts perfectly on the packaged library and `hdr on`
# then fails for a reason that has nothing to do with the patch.
#
# pgrep -n, the newest, and not `| head -1`, the lowest pid. A restart can leave
# the old sway alive for a moment, and the lowest pid is then the one that was
# started before any of this existed - which reads exactly like the drop-in
# having failed.
pid=$(insh "pgrep -n -u $PLAYER -x sway")
if [ -z "$pid" ]; then
    warn "no sway process found to ask"
    exit 1
fi

loaded=$(insh "grep -o '/[^ ]*libwlroots[^ ]*' /proc/$pid/maps 2>/dev/null | sort -u")
echo "$loaded" | sed 's/^/    /'

if grep -q "^$PREFIX/" <<< "$loaded"; then
    ok "sway is running against the patched wlroots"
else
    bad "sway loaded the packaged wlroots, not the patched one"

    step "Why the loader ignored it"
    # Three things make ld.so skip LD_LIBRARY_PATH or lose to it, and they are
    # told apart here rather than guessed at.
    echo "    does the process itself have the variable:"
    insh "tr '\0' '\n' < /proc/$pid/environ 2>/dev/null | grep -i '^LD_LIBRARY_PATH=' || echo '(not in the process environment)'" | sed 's/^/      /'

    echo "    is sway in secure-execution mode, where ld.so drops LD_LIBRARY_PATH:"
    insh "ls -l /usr/bin/sway; getcap /usr/bin/sway 2>/dev/null || echo '(getcap not installed)'" | sed 's/^/      /'

    echo "    does sway carry an RPATH, which beats LD_LIBRARY_PATH:"
    insh "objdump -p /usr/bin/sway 2>/dev/null | grep -E 'RPATH|RUNPATH' || echo '(neither)'" | sed 's/^/      /'

    echo "    is the built library where it is supposed to be, and loadable:"
    insh "ls -l $PREFIX/lib/libwlroots-0.20.so* 2>/dev/null; file $PREFIX/lib/libwlroots-0.20.so 2>/dev/null" | sed 's/^/      /'

    exit 1
fi

ok "gate two passed, continue with 30-hdr.sh"
