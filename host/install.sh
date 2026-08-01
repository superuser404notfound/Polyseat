#!/usr/bin/env bash
# Installs Polyseat from this checkout.
#
# Two halves, and they are separate because only one of them can be packaged.
#
# Getting the machine ready is prepare.sh, run from here: packages, the idmap
# range, Incus, the driver, the group. An Arch package is not allowed to do any
# of that, so somebody installing from a package runs polyseat-prepare by hand
# and there is one copy of it either way.
#
# Placing the files is what is left in this script, and it is exactly what a
# package would do instead: build the daemon, put the input helpers, the udev
# rule and the systemd unit where they belong, and restart a daemon that was
# already running.
#
# Creating and configuring seats is neither of those. That belongs to the daemon
# and its web interface. Who does what, and why the line runs where it does, is
# in docs/installation.md.
#
# Arch-based only: it queries pacman.
#
#   sudo ./install.sh                    install
#   sudo ./install.sh --uninstall        remove Polyseat, keep the seats
#   sudo ./install.sh --purge            remove the seats as well
#   sudo ./install.sh --purge --library  and the shared game library
#
# --yes answers the question --purge asks.
set -euo pipefail

BINDIR=/usr/local/bin
LIBDIR=/usr/local/lib/polyseat
UNITDIR=/etc/systemd/system
RULEDIR=/etc/udev/rules.d
HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd -- "$HERE/.." && pwd)"
SRC="$REPO/spike/m2-input-broker"

ok()   { printf '  \033[32m✓\033[0m %s\n' "$*"; }
bad()  { printf '  \033[31m✗\033[0m %s\n' "$*"; }
warn() { printf '  \033[33m!\033[0m %s\n' "$*"; }
step() { printf '\n\033[1m%s\033[0m\n' "$*"; }

[[ $EUID -eq 0 ]] || { echo "needs root"; exit 1; }

STATEDIR=/var/lib/polyseat
LIBRARYDIR=/srv/polyseat/library

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

if [[ "${1:-}" == "--purge" ]]; then
    shift

    library=false
    assume_yes=false

    for arg in "$@"; do
        case "$arg" in
            --library) library=true ;;
            --yes) assume_yes=true ;;
            *) echo "unknown option: $arg"; exit 1 ;;
        esac
    done

    # The names come from the seat records and from nowhere else. Matching
    # container names against a pattern would put somebody's unrelated container
    # one typo away from being deleted.
    seats=()
    if [[ -d $STATEDIR/seats ]]; then
        for f in "$STATEDIR"/seats/*.json; do
            [[ -e $f ]] || continue
            seats+=("$(basename "$f" .json)")
        done
    fi

    step "This removes Polyseat and everything it built"
    echo "  seats to delete:      ${seats[*]:-none found}"
    echo "  daemon state:         $STATEDIR (seat definitions, pairings, the web password)"
    if $library; then
        echo "  shared game library:  $LIBRARYDIR, and its games with it"
    else
        echo "  shared game library:  $LIBRARYDIR is KEPT, so the games come back"
    fi
    echo "  packages and Incus:   left alone, they are not Polyseat's to remove"
    echo

    if ! $assume_yes; then
        read -r -p "  Type purge to go ahead: " answer
        [[ $answer == "purge" ]] || { echo "  nothing done"; exit 1; }
    fi

    # The daemon first, and this is the whole reason --purge exists rather than
    # being a paragraph in a document. It supervises every seat and reads inside
    # each running one every ten seconds; deleting a container underneath that
    # lands an exec in a shutdown, and Incus answers with a "Stopping instance"
    # task that never finishes. That is how this machine's seats had to be taken
    # apart by hand, and the order is the fix.
    step "Stopping the daemon before touching anything it owns"
    systemctl disable --now polyseatd.service 2>/dev/null || true
    ok "polyseatd stopped"

    if ((${#seats[@]})); then
        step "Deleting the seats"

        for name in "${seats[@]}"; do
            stop_seat "$name"

            if [[ -n "$(state_of "$name")" ]]; then
                timeout 180 incus delete -f "$name" >/dev/null 2>&1 && ok "$name deleted" ||
                    bad "$name could not be deleted"
            else
                ok "$name had no container"
            fi
        done
    fi

    step "Removing what the daemon kept"
    rm -rfv "$STATEDIR" /etc/polyseat

    if $library; then
        rm -rfv "$LIBRARYDIR"
    else
        ok "$LIBRARYDIR kept"
    fi

    # Everything the plain uninstall does, by running that path. The marker is
    # so that it does not sign off by promising the seats are untouched, having
    # just deleted them.
    POLYSEAT_PURGED=1 exec "$0" --uninstall
fi

if [[ "${1:-}" == "--uninstall" ]]; then
    step "Removing"
    systemctl disable --now polyseatd.service 2>/dev/null || true
    # The template units from before the daemon existed, in case this is an
    # upgrade from that state.
    systemctl disable --now 'polyseat-broker@*' polyseat-uhid-observer.service 2>/dev/null || true
    rm -fv "$UNITDIR/polyseatd.service" \
           "$UNITDIR/polyseat-uhid-observer.service" \
           "$UNITDIR/polyseat-broker@.service"
    rm -fv "$BINDIR/polyseatd"
    rm -rfv "$LIBDIR"
    rm -fv "$RULEDIR/70-polyseat-hide.rules" \
           "$RULEDIR/72-polyseat-hide.rules"
    systemctl daemon-reload
    udevadm control --reload
    if [[ -n "${POLYSEAT_PURGED:-}" ]]; then
        ok "gone, seats and all. $LIBRARYDIR is the only thing that may be left."
    else
        ok "gone. Seats, their containers and $STATEDIR are untouched."
    fi

    # Said here because the order matters and is not obvious. The daemon has just
    # been stopped, so the containers can be removed safely now; doing it while it
    # was still running is what leaves Incus with a stop that never finishes.
    if [[ -d $STATEDIR/seats ]] && compgen -G "$STATEDIR/seats/*.json" >/dev/null; then
        echo
        echo "  The seats are still here. Now that the daemon is stopped they can be"
        echo "  removed safely, or leave them and they come back on the next install:"
        echo
        echo "    sudo $0 --purge"
    fi

    exit 0
fi

step "Preparing the machine"
# The machine half lives in its own script, because a package can place files
# and cannot initialise Incus, write to /etc/subuid or put an account in a
# group. One copy of that, two ways in: from a package somebody runs
# polyseat-prepare by hand, and here it is run for them.
#
# It exits non-zero when something is wrong that this cannot fix, and set -e
# then stops the install before anything has been placed, which is the order
# that matters: refuse before changing.
echo "  running $HERE/prepare.sh"

# The variable stops it from signing off with what to do next. What to do next
# is the rest of this script, and it is about to happen.
POLYSEAT_INSTALLING=1 "$HERE/prepare.sh"

step "Building polyseatd"
# Built here rather than shipped as a binary: a binary of unknown provenance
# running as root is worse than a compiler, and the compiler is a requirement of
# this project anyway.
#
# The version is taken from git rather than from a file in the tree. A number
# written down somewhere can disagree with the tag it was cut from; one derived
# from the tag cannot. `safe.directory` is passed because this runs as root over
# a repository owned by somebody else, which is exactly the case git refuses to
# look at by default.
#
# A source tarball has no .git and leaves this at "unknown". Saying so is better
# than inventing a number: the daemon has no other way to find out what it is,
# and an update check that compares against a guess is worse than none.
version=$(git -C "$REPO" -c safe.directory="$REPO" describe --tags --always --dirty 2>/dev/null || true)
[[ -n $version ]] || version=unknown

( cd "$REPO" && go build \
    -ldflags "-X github.com/superuser404notfound/Polyseat/internal/version.Version=$version" \
    -o "$BINDIR/polyseatd" ./cmd/polyseatd )
chmod 0755 "$BINDIR/polyseatd"
ok "$BINDIR/polyseatd $("$BINDIR/polyseatd" -version | awk '{print $2}')"

# Written as `if` and not as `[[ ]] && warn`, because under `set -e` a test that
# comes out false ends the whole script.
if [[ $version == unknown ]]; then
    warn "built from a tree without git history, so it cannot name its own version"
elif [[ $version == *-dirty ]]; then
    warn "built from a tree with uncommitted changes, which is what -dirty means"
fi

step "Installing the input helpers to $LIBDIR"
# These stay Python. They are what M2 proved out, and rewriting a working input
# broker at the same time as writing the daemon around it would have meant
# debugging both at once.
install -d -m 0755 "$LIBDIR"
for f in broker.py device_owner.py uhid_observer.py fakeudev.py; do
    install -m 0755 "$SRC/$f" "$LIBDIR/$f"
    ok "$f"
done

step "udev rule"
# The number matters. Sunshine's own rules hand its virtual devices to the
# desktop user, which is right for a Sunshine on this machine and wrong for one
# in a seat, and undoing that has to happen after they have run. 70-uaccess
# sorts after 70-polyseat did, so this used to lose the race and the tag went
# straight back on.
rm -f "$RULEDIR/70-polyseat-hide.rules"
install -m 0644 "$HERE/72-polyseat-hide.rules" "$RULEDIR/72-polyseat-hide.rules"
udevadm control --reload
ok "installed and reloaded"

step "systemd"
# The old per-seat broker template and the observer unit are gone: the daemon
# supervises both itself, so that the seat lifecycle has exactly one owner.
if [[ -e "$UNITDIR/polyseat-broker@.service" || -e "$UNITDIR/polyseat-uhid-observer.service" ]]; then
    systemctl disable --now 'polyseat-broker@*' polyseat-uhid-observer.service 2>/dev/null || true
    rm -f "$UNITDIR/polyseat-broker@.service" "$UNITDIR/polyseat-uhid-observer.service"
    warn "removed the old broker and observer units, polyseatd runs those now"
fi
install -m 0644 "$HERE/polyseatd.service" "$UNITDIR/polyseatd.service"
systemctl daemon-reload
ok "registered"

step "Restarting the daemon"
# The build wrote a new binary. It did not touch the process that is using the
# old one, and nothing else here does either: without this step an update
# installs cleanly, reports success and changes nothing at all until somebody
# works out that they have to restart it themselves.
#
# Only a daemon that is already running gets restarted. Starting one here would
# bring up Polyseat on a machine whose owner has not finished installing it, and
# the closing message asks them to do that when they are ready.
if systemctl is-active --quiet polyseatd.service; then
    systemctl restart polyseatd.service
    ok "restarted, so the new build is the one serving"
    echo "    A seat that was streaming keeps running, but its input broker is"
    echo "    restarted with the daemon, so a controller can drop for a moment."
else
    ok "not running, so there is nothing to restart"
fi

cat <<EOF

Installed. Start it with:

  systemctl enable --now polyseatd

Then open the interface and create your seats there:

  https://$(hostname):47800

It has no password yet, so the page asks you to choose one rather than to type
one. Do that before anybody else on the network does.

It answers on the whole network and the certificate is self signed, so the
browser asks once, exactly like Sunshine's own interface. To keep it on this
machine instead, set "listen" to "127.0.0.1:47800" in
/etc/polyseat/polyseatd.json.

Check the host afterwards with:

  $HERE/check-hardening.sh
EOF
