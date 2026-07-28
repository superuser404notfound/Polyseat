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
    rm -fv "$RULEDIR/70-polyseat-hide.rules"
    systemctl daemon-reload
    udevadm control --reload
    ok "gone. Seats, their containers and /var/lib/polyseat are untouched."
    exit 0
fi

step "Prerequisites"
missing=()
for pkg in incus nvidia-container-toolkit bpftrace python go; do
    if pacman -Qq "$pkg" >/dev/null 2>&1; then ok "$pkg"
    else bad "$pkg missing"; missing+=("$pkg"); fi
done
if ((${#missing[@]})); then
    echo
    echo "  sudo pacman -S --needed ${missing[*]}"
    exit 1
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
install -m 0644 "$HERE/70-polyseat-hide.rules" "$RULEDIR/70-polyseat-hide.rules"
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

cat <<EOF

Installed. Start it with:

  systemctl enable --now polyseatd

Then open the interface and create your seats there:

  http://127.0.0.1:47800

It listens on localhost only. The daemon creates containers as root, so putting
it on the network is a decision to make on purpose, in
/etc/polyseat/polyseatd.json.

Check the host afterwards with:

  $HERE/check-hardening.sh
EOF
