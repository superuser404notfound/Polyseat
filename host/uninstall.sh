#!/usr/bin/env bash
# Removes Polyseat from this machine.
#
# Split out of install.sh for the same reason prepare.sh was, and it buys the
# same thing: one copy of the procedure and three ways in. install.sh runs this
# file for --uninstall and --purge, the package installs it as
# polyseat-uninstall so that a machine which never had a checkout still has a
# way out, and the daemon runs it in a transient unit so that there is a way out
# without a terminal at all.
#
# The order is the part worth having in one place. The daemon supervises every
# seat and reads inside each running one every ten seconds; deleting a container
# while that is going on lands an exec in a shutdown, and Incus answers with a
# "Stopping instance" task that never finishes. On this machine that took a
# restart of the Incus daemon and killing the container's cgroup by hand to get
# out of, twice. So: stop the daemon, then stop the seats, then delete them, and
# only then take the files away.
#
#   sudo polyseat-uninstall                     the daemon goes, the seats stay
#   sudo polyseat-uninstall --seats             the seats and their state go too
#   sudo polyseat-uninstall --seats --library   and the shared game library
#
# --yes answers the question --seats asks. Nothing else asks anything, and
# nothing here reads from stdin unless there is a terminal to read it from.
#
# What is deliberately not removed: Incus, bpftrace, python, and every other
# package. They are not Polyseat's to remove, and an uninstaller that takes
# somebody's container manager away because it once installed it is an
# uninstaller nobody should run. That is also why the package is removed with
# -R and not -Rs on Arch: on a machine where pacman pulled incus in as a
# dependency of polyseat rather than being told to install it, the s would take
# it away. The other two package managers need their own spelling of the same
# restraint and one of them needs it explicitly — see pkg_remove in distro.sh,
# where dnf's default is the dangerous one.
set -euo pipefail

# Where distro.sh is, resolved here and carried across the copy below.
#
# It has to happen before that copy and not after. The copy runs from /tmp, so a
# lookup made from inside it cannot find the file next to the script the way
# prepare.sh does: by then there is no script next to anything. Resolved while
# ${BASH_SOURCE[0]} still points at the real file, and exported for the copy.
if [[ -z ${POLYSEAT_DISTRO_LIB:-} ]]; then
    _here="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

    for _d in "$_here" /usr/local/lib/polyseat /usr/lib/polyseat; do
        if [[ -r $_d/distro.sh ]]; then
            export POLYSEAT_DISTRO_LIB="$_d/distro.sh"
            break
        fi
    done
fi

# The package manager is about to delete the file bash is reading.
#
# bash does not read a script into memory. It reads a chunk, runs it, and comes
# back for more at a command boundary, so removing the package that owns this
# file halfway down leaves it reading from an inode nothing points at any more,
# and the failure would land in whichever line happened to be in the next chunk.
# So the first thing this does is copy itself somewhere no package owns and
# hand over to that copy. The same reasoning covers distro.sh, which the package
# also owns: it is sourced into this shell before the removal, and a function
# already defined does not care that the file it came from has gone.
if [[ -z ${POLYSEAT_UNINSTALL_COPY:-} ]]; then
    copy=$(mktemp /tmp/polyseat-uninstall.XXXXXX)
    cat -- "${BASH_SOURCE[0]}" > "$copy"
    export POLYSEAT_UNINSTALL_COPY=$copy

    # bash "$copy" rather than exec-ing it directly, so that a /tmp mounted
    # noexec does not turn this into a permission error at the worst moment.
    exec bash "$copy" "$@"
fi

trap 'rm -f -- "${POLYSEAT_UNINSTALL_COPY:-}"' EXIT

if [[ -n ${POLYSEAT_DISTRO_LIB:-} && -r ${POLYSEAT_DISTRO_LIB} ]]; then
    # shellcheck source=host/distro.sh
    . "$POLYSEAT_DISTRO_LIB"
fi

if ! declare -f distro_detect >/dev/null 2>&1; then
    echo "distro.sh was not found next to this script, in /usr/local/lib/polyseat"
    echo "or in /usr/lib/polyseat, and nothing here can ask a question without it."
    exit 1
fi

# Unlike prepare.sh this does not refuse a machine it does not recognise, and
# the asymmetry is deliberate. Preparing an unknown host would install packages
# with a package manager this project cannot drive; removing one only has to
# take files away, which is the same work everywhere. So an unrecognised machine
# gets the whole of the cleanup and skips exactly the step that needs a package
# manager, rather than being left with the daemon it wanted rid of.
has_pkg_manager=true
if ! distro_detect; then has_pkg_manager=false; fi

# Colour only where somebody is watching, the same test prepare.sh makes and for
# the same reason: the daemon runs this in a transient unit and the journal is
# where it lands.
if [[ -t 1 && -z ${NO_COLOR:-} ]]; then
    green=$'\033[32m'; red=$'\033[31m'; yellow=$'\033[33m'
    bold=$'\033[1m';  plain=$'\033[0m'
else
    green=""; red=""; yellow=""; bold=""; plain=""
fi

ok()   { printf '  %s✓%s %s\n' "$green" "$plain" "$*"; }
bad()  { printf '  %s✗%s %s\n' "$red" "$plain" "$*"; }
warn() { printf '  %s!%s %s\n' "$yellow" "$plain" "$*"; }
step() { printf '\n%s%s%s\n' "$bold" "$*" "$plain"; }

STATEDIR=/var/lib/polyseat
CONFIGDIR=/etc/polyseat
LIBRARYDIR=/srv/polyseat/library

seats=false
library=false
assume_yes=false

for arg in "$@"; do
    case "$arg" in
        # --purge is what install.sh called this before it was a file of its
        # own, and it is still what the readme says. Kept as a spelling of
        # --seats rather than removed, because a command somebody has written
        # down should go on working.
        --seats|--purge) seats=true ;;
        --library) library=true ;;
        --yes) assume_yes=true ;;
        -h|--help)
            # The header of this file, up to the first line that is not a
            # comment. A line range would be a number to keep in step with a
            # comment nobody would think to renumber.
            awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' \
                "${BASH_SOURCE[0]}"
            exit 0
            ;;
        *) echo "unknown option: $arg"; exit 1 ;;
    esac
done

[[ $EUID -eq 0 ]] || { echo "needs root"; exit 1; }

# The library is inside the seats' half of this, not beside it. Removing the
# pool while leaving the seats that mount it would take the games out from under
# a working installation, which is not a thing anybody means to ask for.
if $library && ! $seats; then
    echo "--library only means something with --seats: it is the pool the seats share"
    exit 1
fi

# state_of reports what Incus thinks of a container: RUNNING, STOPPED, ERROR or
# nothing at all.
#
# Asked of `incus list` rather than `incus info`, because a container whose stop
# has half finished answers `incus info` with "Invalid PID -1" and nothing else,
# which is exactly the case this has to survive.
state_of() {
    incus list "$1" -c ns -f csv 2>/dev/null | awk -F, -v n="$1" '$1 == n { print $2 }'
}

# stop_seat brings a container down, and insists.
#
# `incus stop` has been seen to return success and leave the container running,
# with a "Stopping instance" task of its own that never ends. Waiting longer does
# not help: what does is killing the processes in the container's cgroup and
# restarting the Incus daemon, which clears the stuck task. Both were needed to
# take this machine's seats down, so both are written here rather than left for
# the next person to work out under pressure.
stop_seat() {
    local name=$1 waited=0

    [[ -n "$(state_of "$name")" ]] || return 0

    # Under a timeout, because this call is the one that hangs. `incus stop` has
    # been seen to sit there for minutes with the server's own "Stopping
    # instance" task never finishing, and without a bound the loop below, which
    # exists precisely for that case, is never reached: the first version of this
    # waited three minutes inside the call it was supposed to be recovering from.
    timeout 45 incus stop "$name" >/dev/null 2>&1 || true

    while [[ "$(state_of "$name")" == "RUNNING" && $waited -lt 60 ]]; do
        sleep 2
        waited=$((waited + 2))
    done

    [[ "$(state_of "$name")" == "RUNNING" ]] || { ok "$name stopped"; return 0; }

    warn "$name did not stop when asked, killing what is left of it"

    local cg=/sys/fs/cgroup/lxc.payload.$name

    if [[ -e $cg/cgroup.kill ]]; then
        echo 1 > "$cg/cgroup.kill" 2>/dev/null || true
    elif [[ -e $cg/cgroup.procs ]]; then
        while read -r pid; do kill -9 "$pid" 2>/dev/null || true; done < "$cg/cgroup.procs"
    fi

    sleep 3
    systemctl restart incus.service 2>/dev/null || true
    sleep 5

    [[ "$(state_of "$name")" == "RUNNING" ]] && bad "$name is still running" || ok "$name stopped"
}

# Which of the two installs this is, asked of the package manager rather than
# guessed from a path. Both answers can be true at once on a machine where somebody built from
# a checkout over a package, and then both have to go: the daemon prefers
# /usr/local, so leaving that half behind would leave the machine running the
# copy this was meant to remove.
#
# Both written as `if` rather than as `test && set`, because under set -e a bare
# test that comes out false is not a false answer, it is the end of the script.
packaged=false
if pkg_installed polyseat; then packaged=true; fi

checkout=false
if [[ -e /usr/local/bin/polyseatd ]]; then checkout=true; fi

# The names come from the seat records and from nowhere else. Matching container
# names against a pattern would put somebody's unrelated container one typo away
# from being deleted.
found=()
if [[ -d $STATEDIR/seats ]]; then
    for f in "$STATEDIR"/seats/*.json; do
        [[ -e $f ]] || continue
        found+=("$(basename "$f" .json)")
    done
fi

step "What this removes"

if $packaged && $checkout; then
    echo "  installed:            the package and a checkout install, both"
elif $packaged; then
    echo "  installed:            the polyseat package"
elif $checkout; then
    echo "  installed:            a checkout install under /usr/local"
else
    echo "  installed:            no package owns it and /usr/local has none, so only leftovers"
fi

echo "  daemon:               stopped, its unit, udev rule and helpers removed"

if $seats; then
    echo "  seats to delete:      ${found[*]:-none found}"
    echo "  daemon state:         $STATEDIR and $CONFIGDIR (seats, pairings, the password)"
else
    echo "  seats:                KEPT, and $STATEDIR with them"
fi

if $library; then
    echo "  shared game library:  $LIBRARYDIR, and its games with it"
else
    echo "  shared game library:  $LIBRARYDIR is KEPT, so the games come back"
fi

echo "  packages and Incus:   left alone, they are not Polyseat's to remove"
echo

# Only the destructive half asks, and only where there is somebody to answer.
# Without a terminal the answer has to have come with the command, or this stops
# rather than deleting seats nobody confirmed.
if $seats && ! $assume_yes; then
    if [[ ! -t 0 ]]; then
        bad "deleting the seats needs --yes when there is nobody at a terminal to ask"
        exit 1
    fi

    read -r -p "  Type remove to go ahead: " answer
    [[ $answer == "remove" ]] || { echo "  nothing done"; exit 1; }
fi

# The daemon first, and this is the whole reason the order is written down. It
# supervises every seat and reads inside each running one every ten seconds;
# deleting a container underneath that lands an exec in a shutdown, and Incus
# answers with a "Stopping instance" task that never finishes.
step "Stopping the daemon before touching anything it owns"
systemctl disable --now polyseatd.service 2>/dev/null || true
# The template units from before the daemon existed, in case this installation
# goes back that far.
systemctl disable --now 'polyseat-broker@*' polyseat-uhid-observer.service 2>/dev/null || true
ok "polyseatd stopped"

if $seats && ((${#found[@]})); then
    step "Deleting the seats"

    for name in "${found[@]}"; do
        stop_seat "$name"

        if [[ -n "$(state_of "$name")" ]]; then
            timeout 180 incus delete -f "$name" >/dev/null 2>&1 && ok "$name deleted" ||
                bad "$name could not be deleted"
        else
            ok "$name had no container"
        fi
    done
fi

if $seats; then
    step "Removing what the daemon kept"
    rm -rfv "$STATEDIR" "$CONFIGDIR"

    if $library; then
        rm -rfv "$LIBRARYDIR"
    else
        ok "$LIBRARYDIR kept"
    fi
fi

# What a checkout install placed. Removed whether or not pacman also owns a copy
# elsewhere, because the daemon looks in /usr/local first and a half removal
# would leave the machine running exactly what this was asked to take away.
if $checkout; then
    step "Removing the checkout install"
    rm -fv /usr/local/bin/polyseatd \
           /usr/local/bin/polyseat-prepare \
           /usr/local/bin/polyseat-uninstall \
           /usr/local/bin/polyseat-lan-bridge \
           /etc/systemd/system/polyseatd.service \
           /etc/systemd/system/polyseat-uhid-observer.service \
           /etc/systemd/system/polyseat-broker@.service \
           /usr/local/share/applications/polyseat.desktop \
           /usr/local/share/icons/hicolor/scalable/apps/polyseat.svg
    rm -rfv /usr/local/lib/polyseat
    # Placed by that installer, so removed by it. The module itself is left
    # loaded: unloading it would reach past this installation, since uhid is
    # what bluez uses for HID over GATT.
    rm -fv /usr/local/lib/modules-load.d/polyseat.conf
fi

# The copy 0.3.2 to 0.3.4 wrote by hand from prepare.sh, into a directory no
# package owns. Taken out here whichever way this was installed, because no
# package manager can remove a file that was never part of a package and a
# machine that keeps loading a module for something that is gone is the result.
rm -fv /etc/modules-load.d/polyseat.conf

# The rule the checkout install writes into /etc. The package's copy lives in
# /usr/lib and goes with the package, so this is only ever the local one, plus
# the 70- name it had before the number turned out to matter.
rm -fv /etc/udev/rules.d/70-polyseat-hide.rules \
       /etc/udev/rules.d/72-polyseat-hide.rules

if $packaged; then
    step "Removing the package"

    # Answering no question and taking no dependencies with it: see the top of
    # this file, and pkg_remove for how each of the three is made to behave that
    # way. What it leaves behind is incus, bpftrace and python, which is the
    # right amount of somebody else's machine for an uninstaller to touch.
    if ! $has_pkg_manager; then
        warn "this machine's package manager is not one Polyseat knows"
        echo "    Everything above is done. The package itself is still registered,"
        echo "    and removing it is one command in whatever this machine uses."
    elif pkg_remove polyseat; then
        ok "the polyseat package is gone"
    else
        bad "the package manager would not remove the package"
        echo "    A locked database is the usual reason: another one is running."
        echo "    Nothing above is undone by this, so running the same command"
        echo "    again once it is free finishes the job."
        exit 1
    fi
fi

# Both allowed to fail rather than ending the run on the last line before the
# summary. A machine where these two do not answer is a machine where the
# `systemctl disable` at the top did not either, and everything between them has
# already happened.
systemctl daemon-reload 2>/dev/null || true
udevadm control --reload 2>/dev/null || true

step "Done"

if $seats; then
    ok "gone, seats and all"

    if $library; then
        ok "$LIBRARYDIR went with them"
    else
        ok "$LIBRARYDIR is kept, so the games come back with the next install"
    fi
else
    ok "gone. Seats, their containers and $STATEDIR are untouched"

    if ((${#found[@]})); then
        echo
        echo "  The seats are still here: ${found[*]}. Installing Polyseat again picks"
        echo "  them up where they were."
        echo
        # Not "run this again with --seats". This file went with the package a
        # moment ago, and pointing at a command that no longer exists is how an
        # uninstaller ends by wasting somebody's afternoon. Incus owns the
        # containers and is still here.
        echo "  To take them now instead, the daemon is stopped and Incus owns them:"
        echo
        echo "    sudo incus delete -f ${found[*]}"
        echo "    sudo rm -rf $STATEDIR $CONFIGDIR"
    fi
fi
