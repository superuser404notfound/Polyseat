# shellcheck shell=bash
# shellcheck disable=SC2034  # sourced: what looks unused here is used by the callers
# Shared definitions for the M8 scripts.
#
# Unlike M1, this spike does not build a seat. It changes one that polyseatd has
# already provisioned, and every change is a file the daemon does not write, so
# that 99-cleanup.sh can take all of it back out and a provisioning run does not
# fight with it.
CT="${CT:-seat1}"                  # name of the Incus container
PLAYER="${PLAYER:-player}"         # the user the session runs as
HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

# Where the spike's own builds go. /usr/local because the packaged wlroots and
# the packaged Sunshine stay exactly where they are: this spike is meant to be
# reversible by deleting files, not by reinstalling packages.
PREFIX=/usr/local

# The upstream versions the patches were written against, and the reason they
# are pinned rather than tracking a branch: a patch that applies to whatever
# master happens to be today is a patch that stops applying tomorrow, and the
# first symptom would be a build that quietly still contains the old code.
#
# wlroots 0.20.2 is what Arch ships as wlroots0.20, which is what sway 1.12
# links against, so the rebuilt library has the soname the packaged sway is
# already looking for.
WLROOTS_TAG="${WLROOTS_TAG:-0.20.2}"
SUNSHINE_COMMIT="${SUNSHINE_COMMIT:-fa462d250bf19fb3ea7d6c9447023f4e61fa5053}"

# After `usermod -aG incus-admin` the running session is still missing the group
# until the next login, so a script started in the shell that ran usermod fails
# for a reason that has nothing to do with the script.
#
# The older spikes re-exec through `sg` here. That only works where `sg` exists,
# and it does not exist everywhere: `sg` ships with shadow, and on a machine
# whose `newgrp` comes from util-linux instead there is no `sg` at all. The
# symptom was ugly and pointed nowhere - "exec: sg: not found", from lib.sh,
# before any script had said what it wanted.
#
# So: re-exec where that is possible, and otherwise say the one command that
# fixes it. util-linux's newgrp takes no command to run, which is why it cannot
# stand in for sg here.
# Here-strings rather than pipes into grep -q, for the reason spelled out in
# 10-vulkan.sh: with pipefail, grep -q leaving early can kill the writer with
# SIGPIPE and the 141 becomes the answer to the question. The inputs here are
# one line each and would almost always win the race, which is worse than
# always losing it, not better.
if ! grep -qx incus-admin <<< "$(id -nG | tr ' ' '\n')" \
   && grep -qw "$USER" <<< "$(getent group incus-admin)"; then
    if command -v sg >/dev/null 2>&1; then
        exec sg incus-admin -c "$(printf '%q ' "$0" "$@")"
    fi

    cat >&2 <<MSG
polyseat spike: $USER is in incus-admin, but this shell is not.

  usermod records the membership; a shell only picks it up at login. This
  machine has no \`sg\`, so it cannot be borrowed for one command.

  Start a shell that has the group and run the script again:

      newgrp incus-admin

  A fresh login or a new terminal works just as well. Failing that:

      INCUS_CMD="sudo incus" $0

MSG
    exit 1
fi

# How incus is invoked. An array because it may be more than one word.
#
# The group above is the pleasant way in and the only one worth setting up: this
# spike calls incus dozens of times per script, so `INCUS_CMD="sudo incus"` will
# ask for a password dozens of times unless sudo is already caching one. It is
# here for the machine where adding a group membership is not on the table.
read -r -a INCUS <<< "${INCUS_CMD:-incus}"

ok()   { printf '  \033[32m✓\033[0m %s\n' "$*"; }
bad()  { printf '  \033[31m✗\033[0m %s\n' "$*"; }
warn() { printf '  \033[33m!\033[0m %s\n' "$*"; }
step() { printf '\n\033[1m%s\033[0m\n' "$*"; }

# Run something in the seat as root.
inseat() { "${INCUS[@]}" exec "$CT" -- "$@"; }

# Run a shell line in the seat as root.
insh() { "${INCUS[@]}" exec "$CT" -- sh -c "$1"; }

# Run something in the seat as the player, with the session's environment.
#
# SWAYSOCK is resolved rather than assumed: the socket carries sway's pid in its
# name, so it changes on every restart, and this spike restarts sway repeatedly.
as_player() {
    local sock wl
    sock="$(insh 'ls -t /run/user/1000/sway-ipc.*.sock 2>/dev/null | head -1')"

    # WAYLAND_DISPLAY too, and newest first, for the same reason polyseat's own
    # sunshine-run.sh looks it up rather than trusting an imported value: the
    # socket carries no fixed name and a restart leaves the old one lying about.
    #
    # Without it any ordinary Wayland client run through here fails to connect
    # and says so on stderr, which is usually discarded - so the symptom is a
    # step that prints nothing at all and looks like a question with no answer
    # rather than a question that was never asked.
    wl="$(insh 'find /run/user/1000 -maxdepth 1 -type s -name "wayland-[0-9]*" -printf "%T@ %f\n" 2>/dev/null | sort -rn | head -1 | cut -d" " -f2-')"

    "${INCUS[@]}" exec "$CT" -- sudo -u "$PLAYER" env \
        XDG_RUNTIME_DIR=/run/user/1000 \
        DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus \
        WAYLAND_DISPLAY="$wl" \
        SWAYSOCK="$sock" "$@"
}

# The log of the sway invocation that is running right now.
#
# Not `journalctl -n <a number>`. sway at debug level writes several hundred
# lines while it starts, and the one line this spike most needs - the buffer
# format it chose for the output - is among the earliest of them. A window large
# enough today is a window that silently stops containing it tomorrow, and the
# symptom is not an error: it is a check that reports "could not read the
# format" and carries on.
#
# systemd stamps every start of a unit with an invocation id, so asking for that
# is exact and needs no window at all. The fallback is for a journal that will
# not answer that way, where a large window is at least better than a small one.
unitlog() {
    local unit=$1 id
    id=$(as_player systemctl --user show -p InvocationID --value "$unit" 2>/dev/null | tr -d '\r')

    if [ -n "$id" ]; then
        as_player journalctl --user "_SYSTEMD_INVOCATION_ID=$id" --no-pager 2>/dev/null
    else
        as_player journalctl --user -u "$unit" --no-pager -n 5000 2>/dev/null
    fi
}

swaylog() { unitlog polyseat-sway.service; }

# The output's pixel format, as the session actually logs it.
#
# Read from the lines that exist rather than the one that seemed likely. wlroots
# has a "Choosing primary buffer format" message, and on this path it is not the
# one that appears; what does appear is sway's own `render_format:` and, under
# it, the allocator and the renderer:
#
#   [sway/config/output.c] render_format: XR24
#   [wlr] [render/allocator/gbm.c] Allocated 1920x1080 GBM buffer with format XR24
#   [wlr] [render/vulkan/renderer.c] vulkan create_render_buffer: XR24, 1920x1080
#
# sway's own line is preferred because it is the decision; the other two are
# consequences of it.
formatline() {
    local log=$1 line
    line=$(grep -E 'render_format:' <<< "$log" | tail -1)
    [ -n "$line" ] || line=$(grep -E 'GBM buffer with format|create_render_buffer' <<< "$log" | tail -1)
    printf '%s' "$line"
}

# Eight bit or ten, from such a line.
#
# Matched on the DRM fourcc names, because that is what drmGetFormatName returns
# and therefore what the log carries: XR24 is XRGB8888 and XR30 is XRGB2101010.
# Looking for the string "2101010" finds nothing, ever, which is exactly the
# mistake this comment exists to stop somebody repeating.
formatdepth() {
    case "$1" in
        *XR30*|*XB30*|*AR30*|*AB30*|*2101010*) echo 10 ;;
        *XR24*|*XB24*|*AR24*|*AB24*|*8888*)    echo 8 ;;
        *)                                     echo "?" ;;
    esac
}

# Wait for a user unit to be active again after a restart.
wait_active() {
    local unit=$1 i=0
    while [ "$i" -lt 60 ]; do
        if [ "$(as_player systemctl --user is-active "$unit" 2>/dev/null)" = active ]; then
            return 0
        fi
        i=$((i + 1))
        sleep 1
    done
    return 1
}
