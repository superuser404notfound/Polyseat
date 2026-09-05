#!/usr/bin/env bash
# Something in the seat that actually produces HDR.
#
# The four steps before this make the seat's display an HDR display and make
# Sunshine send what that display holds. They do not put anything HDR into it.
# On Linux the thing that hands a game an HDR display is gamescope, and its
# Wayland backend is already a complete colour management client: it asks the
# parent compositor for the output's image description, and in
# CWaylandPlane::Wayland_WPImageDescriptionInfo_Done it switches HDR on when the
# target luminance it was told exceeds the reference luminance. wlroots fills
# both from the PQ defaults, 10000 against 203, so with the output in HDR
# gamescope should turn itself on without being argued with.
#
# vkcube by default, because vulkan-tools is already in every seat and this step
# is about the colour of the surface rather than about the picture on it.
# gamescope maps SDR content into the PQ output at --hdr-sdr-content-nits, so
# even an SDR cube proves the path; a real HDR title proves the rest.
set -uo pipefail
source "$(dirname "$0")/lib.sh"

APP="${APP:-vkcube}"
LOG=/tmp/m8-gamescope.log

step "Is gamescope there"
insh 'command -v gamescope >/dev/null' || { bad "gamescope not installed"; exit 1; }
insh 'gamescope --version 2>&1 | head -1' | sed 's/^/    /'

step "Starting gamescope"
# --hdr-enabled asks for an HDR output and falls back to tonemapped SDR when the
# parent will not give one, which is exactly the distinction being measured, so
# --hdr-debug-force-support is deliberately NOT passed. Forcing it would make
# this step succeed whether or not the four before it worked.
insh "rm -f $LOG"

# setsid, and not just a backgrounded subshell. `incus exec` takes the whole
# process group with it when its command returns, so a plain `( ... & )` starts
# gamescope, lets it write a page of log, and then kills it - which reads
# exactly like gamescope having crashed on startup.
as_player sh -c "cd /home/$PLAYER && \
    setsid nohup gamescope --hdr-enabled -W 1920 -H 1080 -f -- $APP > $LOG 2>&1 < /dev/null &" \
    || { bad "could not start gamescope"; exit 1; }

sleep 6

# -x first, then -f. gamescope forks and the main process does not always keep
# `gamescope` as its comm, so an exact match on the name can miss a process that
# is plainly running - sway's tree showed the window while pgrep -x said nothing.
if insh "pgrep -u $PLAYER -x gamescope >/dev/null 2>&1 || pgrep -u $PLAYER -f '^gamescope' >/dev/null 2>&1"; then
    ok "gamescope is running"
else
    warn "no gamescope process found; it may have exited, or it may never have detached"
    insh "tail -20 $LOG" | sed 's/^/    /'
fi

step "What gamescope decided about HDR"
# The last HDR INFO block, not every line that mentions HDR. gamescope prints one
# block per image description it is handed, and the first is its own SDR default
# of 80 nits against 80 - printing them all makes the real answer hard to find.
gslog=$(insh "cat $LOG 2>/dev/null")
last=$(grep -n 'HDR INFO' <<< "$gslog" | tail -1 | cut -d: -f1)
if [ -n "$last" ]; then
    sed -n "${last},$((last + 3))p" <<< "$gslog" | sed 's/^/    /'
else
    grep -i 'hdr' <<< "$gslog" | tail -10 | sed 's/^/    /'
fi

if insh "grep -qi 'bExposeHDRSupport: true' $LOG"; then
    ok "gamescope is offering HDR to what it runs"
elif insh "grep -qi 'bExposeHDRSupport: false' $LOG"; then
    bad "gamescope will not offer HDR to what it runs"

    # Told apart rather than guessed at, because the two causes look identical
    # from here and only one of them is anything to do with this spike.
    #
    # gamescope only uses a compositor's colour management when it advertises
    # the whole set it wants: parametric, set_primaries,
    # set_mastering_display_primaries, extended_target_volume, set_luminances
    # and windows_scrgb. Miss one and CWaylandBackend::SupportsColorManagement()
    # is false, the HDR assignment in Wayland_WPImageDescriptionInfo_Done never
    # runs, and bExposeHDRSupport stays false however good the luminances are.
    if insh "grep -q 'uMaxLum' $LOG"; then
        echo "    the luminances it was handed, last first:"
        insh "grep -E 'uMaxLum' $LOG | tail -2" | sed 's/^/      /'
    fi

    if insh 'command -v wayland-info >/dev/null 2>&1'; then
        # sed and not `grep -o ... | tr -d ' '`: wayland-info indents with tabs,
        # and tr was only asked for spaces, so every name kept its leading tabs
        # and every comparison below said "missing" - including for the two
        # features that were printed as present two lines above it.
        have=$(as_player wayland-info 2>/dev/null \
               | sed -n '/supported features:/,/supported named/p' \
               | sed -n 's/^[[:space:]]\+\([a-z_0-9]\+\)[[:space:]]*$/\1/p')
        echo "    features the compositor advertises:"
        grep -v '^$' <<< "$have" | sed 's/^/      /'
        echo "    features gamescope requires:"
        for f in parametric set_primaries set_mastering_display_primaries \
                 extended_target_volume set_luminances windows_scrgb; do
            if grep -qx "$f" <<< "$have"; then
                echo "      $f"
            else
                echo "      $f   <- missing"
            fi
        done
        warn "  this is sway's choice, not wlroots' limit: sway/server.c asks for"
        warn "  parametric and set_mastering_display_primaries and nothing else,"
        warn "  while wlroots can advertise all six. Neither patch in this spike"
        warn "  changes it, and forcing it with --hdr-debug-force-support would"
        warn "  only tone-map back to SDR, which is not what is being measured."
    fi
else
    warn "gamescope said nothing either way; read $LOG in the seat"
fi

step "What sway sees of it"
as_player swaymsg -t get_tree 2>/dev/null | grep -iE '"app_id"|"name"' | grep -i gamescope | sed 's/^/    /' | head -5

step "And now Sunshine"
warn "connect a Moonlight client with HDR switched on, start the Desktop"
warn "application, then run ./50-verify.sh again"
warn ""
warn "to stop this again:  incus exec $CT -- pkill -u $PLAYER gamescope"
