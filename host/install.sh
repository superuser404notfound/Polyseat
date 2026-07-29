#!/usr/bin/env bash
# Installs the host-side parts of Polyseat.
#
# This is the part a daemon cannot do for itself: put files in place, install a
# udev rule, register a systemd unit. Creating and configuring seats is not done
# here, that belongs to the daemon and its web interface.
#
# Still deliberately small. What the finished installer has to cover, and why
# each step exists, is written down in docs/installation.md; the bootstrap parts
# that turn a fresh machine into one that can run Incus at all are not here yet,
# because they cannot be tested on a machine that is already bootstrapped.
#
# Arch-based only: it queries pacman.
#
#   sudo ./install.sh            install
#   sudo ./install.sh --uninstall
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
    ok "gone. Seats, their containers and /var/lib/polyseat are untouched."
    exit 0
fi

step "Prerequisites"
missing=()
for pkg in incus nvidia-container-toolkit bpftrace python go; do
    if pacman -Qq "$pkg" >/dev/null 2>&1; then ok "$pkg"
    else warn "$pkg missing"; missing+=("$pkg"); fi
done

if ((${#missing[@]})); then
    echo
    echo "  installing: ${missing[*]}"
    echo

    # Deliberately not -Sy. Refreshing the package database and then installing
    # from it without upgrading is the partial upgrade Arch warns about, and an
    # installer is a bad place to break somebody's system in a way that shows up
    # weeks later. So this installs from the database that is already there and
    # says what to do when that database is too old to resolve.
    if pacman -S --needed --noconfirm "${missing[@]}"; then
        ok "installed ${missing[*]}"
    else
        echo
        bad "those packages could not be installed"
        echo "  The package database is probably out of date. Upgrade first:"
        echo
        echo "    sudo pacman -Syu"
        exit 1
    fi
fi

step "idmap ranges"
# Without a root entry in both files every container start fails with "System
# doesn't have a functional idmap setup", which does not mention subuid at all
# and sends people looking in the wrong place. CachyOS ships an entry for the
# user and none for root, so this is not a hypothetical.
#
# An existing entry is left alone even if it is narrower than this one. Widening
# somebody's idmap ranges behind their back would silently change what every
# other container on the machine may map.
for f in /etc/subuid /etc/subgid; do
    if [[ -e $f ]] && grep -qE '^root:' "$f"; then
        ok "$f: $(grep -m1 -E '^root:' "$f")"
    else
        # Appended with a newline of its own, because a file that does not end
        # in one would otherwise gain a joined line rather than a new entry.
        [[ -e $f ]] || : > "$f"
        [[ -s $f && -n "$(tail -c1 "$f")" ]] && printf '\n' >> "$f"
        printf 'root:1000000:1000000000\n' >> "$f"
        ok "$f: added root:1000000:1000000000"
    fi
done

step "Incus"
# The daemon talks to Incus over its socket and cannot bring it up itself, so
# this is squarely the installer's job.
systemctl enable --now incus.socket >/dev/null 2>&1
ok "incus.socket enabled"

# `incus admin init --minimal` is safe to skip and not safe to repeat: run on an
# already initialised machine it fails, and the useful signal that it has been
# initialised is that a storage pool exists.
if incus storage list --format csv 2>/dev/null | grep -q .; then
    ok "already initialised, $(incus storage list --format csv 2>/dev/null | wc -l) storage pool(s)"
else
    # --minimal picks btrfs by itself when the root filesystem is btrfs, which
    # is what the shared game library wants anyway.
    incus admin init --minimal
    ok "initialised with the defaults"
fi

step "Shared game library"
# Reported rather than fixed, and reported here rather than only in the web
# interface after the first seat has been built.
#
# The library shares blocks between seats instead of copying them, which needs a
# filesystem that can do it. That is a property of the mount and the kernel and
# not of the label: XFS only reflinks when it was made with reflink=1, and a
# btrfs subvolume with nodatacow does not either. So this asks the filesystem
# instead of asking its name, the same way the daemon does at startup.
#
# Nothing here fails on a no. Every other part of Polyseat works on any
# filesystem and the library simply stays off, which the daemon says plainly.
LIBDIR_DEFAULT=/srv/polyseat/library
libdir=$LIBDIR_DEFAULT
if [[ -r /etc/polyseat/polyseatd.json ]]; then
    configured=$(python -c 'import json,sys;print(json.load(open("/etc/polyseat/polyseatd.json")).get("library_dir",""))' 2>/dev/null || true)
    [[ -n $configured ]] && libdir=$configured
fi

# The directory does not exist yet on a first install, so the question is asked
# of the nearest ancestor that does, which is the filesystem it will land on.
probe=$libdir
while [[ ! -d $probe ]]; do probe=$(dirname "$probe"); done

fstype=$(findmnt -no FSTYPE --target "$probe" 2>/dev/null || echo unknown)

if scratch=$(mktemp -d "$probe/.polyseat-probe.XXXXXX" 2>/dev/null); then
    head -c 4096 /dev/urandom > "$scratch/a" 2>/dev/null || true

    if cp --reflink=always "$scratch/a" "$scratch/b" 2>/dev/null; then
        ok "$probe is $fstype and shares blocks, the library will work"
    else
        warn "$probe is $fstype and cannot share blocks"
        echo "    Seats will work; installing a game once and playing it in every"
        echo "    seat will not. btrfs can, and so can XFS made with reflink=1."
    fi

    rm -rf "$scratch"
else
    warn "could not write to $probe to find out whether it shares blocks"
fi

step "Network uplink"
# Each seat gets a macvlan interface so that it is a host of its own on the LAN
# and can use the standard Sunshine ports. Two things make that impossible, and
# both are quiet: no default route to take the interface from, and a wireless
# one, where macvlan cannot work at all because 802.11 does not carry more than
# one MAC address per association.
uplink=$(ip -o route show default 2>/dev/null | awk '{print $5; exit}')

if [[ -z $uplink ]]; then
    warn "no default route, so there is no interface for seats to take a macvlan from"
    echo "    Set \"uplink\" in /etc/polyseat/polyseatd.json once the machine has one."
elif [[ -d /sys/class/net/$uplink/wireless || -e /sys/class/net/$uplink/phy80211 ]]; then
    warn "$uplink carries the default route and is wireless"
    echo "    macvlan does not work on wifi. Seats need a wired interface, or a"
    echo "    different \"uplink\" in /etc/polyseat/polyseatd.json."
else
    ok "$uplink carries the default route and seats can take a macvlan from it"
fi

step "Building polyseatd"
# Built here rather than shipped as a binary: there is no release process yet,
# and a binary of unknown provenance running as root is worse than a compiler.
( cd "$REPO" && go build -o "$BINDIR/polyseatd" ./cmd/polyseatd )
chmod 0755 "$BINDIR/polyseatd"
ok "$BINDIR/polyseatd $("$BINDIR/polyseatd" -version | awk '{print $2}')"

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

step "Group membership"
# The daemon does not need this. It runs as root and opens every device node
# directly; that is worth saying plainly, because a step that grants an account
# read access to every input device on the machine should not be there out of
# habit.
#
# What needs it is the host-side tooling that comes with this repository and is
# run by hand: uhidgen.py opens /dev/uhid to make a synthetic gamepad, and
# /dev/uhid and /dev/uinput are root:input 0660. Sunshine running on the host
# itself needs the same two nodes.
#
# On a desktop with an active local session logind already grants that account
# an access control entry on both, which was measured on the development
# machine, so there the group changes nothing. It is the cases without one that
# this covers: a headless host, a second administrator, a session that is not
# the active one. Group membership works in all of them and an ACL works in
# none.
#
# Not undone by --uninstall. It is a property of the account rather than of this
# installation, and an account may well have been in that group first.
target_user=${SUDO_USER:-}
if [[ -z $target_user || $target_user == root ]]; then
    warn "no unprivileged account to add: run this with sudo from your own account"
elif id -nG "$target_user" 2>/dev/null | tr ' ' '\n' | grep -qx input; then
    ok "$target_user is already in the input group"
else
    usermod -aG input "$target_user"
    ok "$target_user added to the input group, which takes effect at the next login"
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
