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
# Runs on Arch, Debian and Fedora, and on anything based on those. Which one
# this is matters only to prepare.sh, which this hands the machine half to;
# placing files is the same work everywhere.
#
#   sudo ./install.sh                    install
#   sudo ./install.sh --uninstall        remove Polyseat, keep the seats
#   sudo ./install.sh --purge            remove the seats as well
#   sudo ./install.sh --purge --library  and the shared game library
#
# --yes answers the question --purge asks. All four of those are uninstall.sh,
# which this hands over to: it is the file the package installs as
# polyseat-uninstall and the one the web interface runs.
set -euo pipefail

BINDIR=/usr/local/bin
LIBDIR=/usr/local/lib/polyseat
UNITDIR=/etc/systemd/system
RULEDIR=/etc/udev/rules.d
MODULESDIR=/usr/local/lib/modules-load.d
HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd -- "$HERE/.." && pwd)"
SRC="$REPO/spike/m2-input-broker"

ok()   { printf '  \033[32m✓\033[0m %s\n' "$*"; }
bad()  { printf '  \033[31m✗\033[0m %s\n' "$*"; }
warn() { printf '  \033[33m!\033[0m %s\n' "$*"; }
step() { printf '\n\033[1m%s\033[0m\n' "$*"; }

[[ $EUID -eq 0 ]] || { echo "needs root"; exit 1; }

# web_url prints the address to open, rather than a placeholder or a host name
# nothing on the network resolves.
#
# The IPv4 address on the interface carrying the default route, which is the one
# another machine here can reach, and the host name when there is no route to
# ask. There is a copy of this in packaging/aur/polyseat.install, which cannot
# source anything: three lines in two places beats a library for two callers.
web_url() {
    local addr

    addr=$(ip -4 route get 1.1.1.1 2>/dev/null |
        awk '{for (i = 1; i <= NF; i++) if ($i == "src") {print $(i + 1); exit}}')

    [[ -n $addr ]] || addr=$(hostname 2>/dev/null)
    [[ -n $addr ]] || addr=this-machine

    printf 'https://%s:47800\n' "$addr"
}


# Removing is uninstall.sh, and this is only the door to it.
#
# One copy of the procedure, for the same reason prepare.sh holds the machine
# half: the package installs that file as polyseat-uninstall so that a machine
# without a checkout has a way out, and the daemon runs it from the web
# interface. A second implementation here would be a second thing to keep in
# step with the first, and the one that gets forgotten is always the one
# somebody is running at the time.
#
# --purge is translated rather than passed through. It is what this script has
# always called it and what the readme says; uninstall.sh takes it as a spelling
# of --seats, so a command somebody wrote down goes on working.
if [[ "${1:-}" == "--uninstall" || "${1:-}" == "--purge" ]]; then
    what=$1
    shift

    args=()
    if [[ $what == --purge ]]; then args+=(--seats); fi

    exec "$HERE/uninstall.sh" "${args[@]}" "$@"
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

step "The package manager table to $LIBDIR"
# distro.sh goes in beside the helpers because the three commands installed
# below source it, and they are installed as /usr/local/bin/polyseat-* where
# there is no checkout next to them to read it from. The package places its own
# copy in /usr/lib/polyseat for the same reason.
install -d -m 0755 "$LIBDIR"
install -m 0644 "$HERE/distro.sh" "$LIBDIR/distro.sh"
ok "$LIBDIR/distro.sh"

step "Installing the input helpers to $LIBDIR"
# These stay Python. They are what M2 proved out, and rewriting a working input
# broker at the same time as writing the daemon around it would have meant
# debugging both at once.
install -d -m 0755 "$LIBDIR"
for f in broker.py device_owner.py uhid_observer.py fakeudev.py; do
    install -m 0755 "$SRC/$f" "$LIBDIR/$f"
    ok "$f"
done

step "Host commands"
# Three of the scripts in host/ go in as commands here as well as in the
# package, and the reason is not tidiness: the daemon looks for them by name.
# The web interface runs polyseat-prepare when somebody presses "Prepare this
# machine", polyseat-uninstall when somebody removes Polyseat from the page and
# polyseat-lan-bridge when somebody puts the uplink on a bridge from the host
# dialog, and a daemon built from a checkout has no way to find the checkout it
# came from. The same lookup serves both installs, /usr/local first and /usr
# second, which is the order the helpers already use.
#
# lan-bridge was not here until the interface learned to run it, and that is the
# rule rather than an exception: a script goes in when something looks for it by
# name. The last one the package ships, polyseat-check-hardening, still does not,
# and whoever has a checkout has it under host/ already.
for cmd in prepare uninstall lan-bridge; do
    install -m 0755 "$HERE/$cmd.sh" "$BINDIR/polyseat-$cmd"
    ok "$BINDIR/polyseat-$cmd"
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
# Loaded at boot so the uhid observer has a symbol to attach to. In
# /usr/local/lib because that is where a checkout install belongs, next to the
# binary and the helpers; the package owns /usr/lib/modules-load.d instead.
# systemd reads both.
install -Dm644 "$HERE/polyseat-modules-load.conf" "$MODULESDIR/polyseat.conf"
ok "$MODULESDIR/polyseat.conf"

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

Installed. Start it:

  sudo systemctl enable --now polyseatd

Then open

  $(web_url)

and choose a password. Nobody has claimed this machine yet, so whoever opens
that page first sets it, and the seats are made there.

The certificate is self signed, so the browser asks about it once. To keep the
page on this machine only, set "listen" to "127.0.0.1:47800" in
/etc/polyseat/polyseatd.json.

Two more commands, for later:

  $HERE/check-hardening.sh
  sudo polyseat-uninstall

The first reports what this host still exposes to a seat. The second takes
Polyseat off again and keeps the seats, unless it is told otherwise.
EOF
