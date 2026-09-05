#!/usr/bin/env bash
# What has to be true before any of the rest is worth running.
#
# Every check here is one that, if it fails, makes a later step fail in a way
# that looks like something else. The versions in particular: sway learned
# `output ... hdr` in 1.12, and against 1.11 the command is simply an unknown
# one, which sway reports as a configuration error somewhere in a file nobody
# was looking at.
set -uo pipefail
source "$(dirname "$0")/lib.sh"

fail=0

step "Can this account talk to Incus at all"
# Asked first and separately, because every other failure below looks the same
# from here: a permission error and a seat that does not exist are both just
# `incus` printing something and returning non-zero.
if ! "${INCUS[@]}" list >/dev/null 2>&1; then
    bad "${INCUS[*]} cannot reach the daemon"
    warn "  the pleasant fix, once, then log in again:"
    warn "    sudo usermod -aG incus-admin \$USER"
    warn "  or, without changing groups:"
    warn "    INCUS_CMD='sudo incus' $0"
    exit 1
fi
ok "${INCUS[*]} answers"

step "The seat"
if "${INCUS[@]}" info "$CT" >/dev/null 2>&1; then
    ok "$CT exists"
else
    bad "there is no seat called $CT"
    echo "    seats on this machine:"
    "${INCUS[@]}" list -c ns --format csv 2>/dev/null | sed 's/^/      /' \
        || echo "      none, or incus would not say"
    warn "  pick one with CT=<name> $0"
    exit 1
fi

state=$("${INCUS[@]}" info "$CT" | awk '/^Status:/ {print $2}')
[ "$state" = RUNNING ] && ok "running" || { bad "not running ($state)"; fail=1; }

step "Versions inside the seat"
sway_ver=$(insh 'sway --version 2>/dev/null' | awk '{print $3}')
case "$sway_ver" in
    1.1[2-9]*|1.[2-9][0-9]*|[2-9].*) ok "sway $sway_ver" ;;
    "") bad "sway not installed"; fail=1 ;;
    *)  bad "sway $sway_ver - HDR needs 1.12 or newer"; fail=1 ;;
esac

wlr_ver=$(insh 'pacman -Q wlroots0.20 2>/dev/null' | awk '{print $2}')
case "$wlr_ver" in
    "") bad "wlroots0.20 not installed - what does sway link against?"; fail=1 ;;
    "$WLROOTS_TAG"-*) ok "wlroots0.20 $wlr_ver, the patch is written against $WLROOTS_TAG" ;;
    *)  warn "wlroots0.20 $wlr_ver, the patch is written against $WLROOTS_TAG"
        warn "  it may still apply; 20-wlroots.sh will say so before it builds" ;;
esac

# Asked with `command -v` and not by reading the version, because gamescope
# prints its version through its own logger and that logger writes to stderr
# (src/log.cpp). Discarding stderr here reported a seat that has gamescope
# installed as one that does not, which is the wrong answer in the direction
# that costs somebody an unnecessary install.
if insh 'command -v gamescope >/dev/null 2>&1'; then
    ok "gamescope: $(insh 'gamescope --version 2>&1' | head -1)"
else
    warn "gamescope missing - 60-gamescope.sh needs it"
fi

step "The renderer HDR needs"
# sway refuses HDR unless the renderer can do output colour transforms, and only
# the Vulkan renderer can. Checked as the presence of a working ICD rather than
# by asking sway, because at this point sway is still on gles2.
if insh 'vulkaninfo --summary >/dev/null 2>&1'; then
    ok "Vulkan works in the seat"
    insh 'vulkaninfo --summary 2>/dev/null | grep -E "deviceName|driverName" | head -4' | sed 's/^/    /'
else
    bad "vulkaninfo fails - the Vulkan renderer cannot work either"
    warn "  on NVIDIA this is usually the missing ICD manifest, see M1"
    fail=1
fi

step "The session"
for u in polyseat-sway polyseat-sunshine; do
    s=$(as_player systemctl --user is-active "$u.service" 2>/dev/null)
    [ "$s" = active ] && ok "$u: $s" || { bad "$u: $s"; fail=1; }
done

echo "    current sway environment:"
as_player systemctl --user show polyseat-sway.service -p Environment 2>/dev/null | sed 's/^/    /'

step "Room to build in"
# wlroots is small. Sunshine is not, and with CUDA it is very much not: the
# toolkit alone is several gigabytes, and it has to be in the seat because that
# is where the build has to run.
insh 'df -h / | tail -1' | sed 's/^/    /'
free=$(insh "df -k / | tail -1 | awk '{print \$4}'")
if [ "${free:-0}" -gt 12000000 ]; then
    ok "$((free / 1024 / 1024)) GB free"
else
    warn "$((free / 1024 / 1024)) GB free - Sunshine with CUDA wants more like 12"
fi

step "Result"
if [ "$fail" = 0 ]; then
    ok "nothing in the way, continue with 10-vulkan.sh"
else
    bad "fix the above first"
    exit 1
fi
