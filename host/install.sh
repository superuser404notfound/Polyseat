#!/usr/bin/env bash
# Installs the host-side parts of Polyseat.
#
# This is the part a daemon cannot do for itself: put files in place, install a
# udev rule, register systemd units. Creating and configuring seats is not done
# here, that belongs to the daemon and its web interface.
#
#   sudo ./install.sh            install
#   sudo ./install.sh --uninstall
set -euo pipefail

LIBDIR=/usr/local/lib/polyseat
UNITDIR=/etc/systemd/system
RULEDIR=/etc/udev/rules.d
HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
SRC="$HERE/../spike/m2-input-broker"

ok()   { printf '  \033[32m✓\033[0m %s\n' "$*"; }
bad()  { printf '  \033[31m✗\033[0m %s\n' "$*"; }
step() { printf '\n\033[1m%s\033[0m\n' "$*"; }

[[ $EUID -eq 0 ]] || { echo "needs root"; exit 1; }

if [[ "${1:-}" == "--uninstall" ]]; then
    step "Removing"
    systemctl disable --now 'polyseat-broker@*' polyseat-uhid-observer.service 2>/dev/null || true
    rm -fv "$UNITDIR/polyseat-uhid-observer.service" "$UNITDIR/polyseat-broker@.service"
    rm -rfv "$LIBDIR"
    rm -fv "$RULEDIR/70-polyseat-hide.rules"
    systemctl daemon-reload
    udevadm control --reload
    ok "gone. Seats and their containers are untouched."
    exit 0
fi

step "Prerequisites"
missing=()
for pkg in incus nvidia-container-toolkit bpftrace python; do
    if pacman -Qq "$pkg" >/dev/null 2>&1; then ok "$pkg"
    else bad "$pkg missing"; missing+=("$pkg"); fi
done
if ((${#missing[@]})); then
    echo
    echo "  sudo pacman -S --needed ${missing[*]}"
    exit 1
fi

step "Installing to $LIBDIR"
install -d -m 0755 "$LIBDIR"
for f in broker.py device_owner.py uhid_observer.py fakeudev.py; do
    install -m 0755 "$SRC/$f" "$LIBDIR/$f"
    ok "$f"
done

step "udev rule"
install -m 0644 "$HERE/70-polyseat-hide.rules" "$RULEDIR/70-polyseat-hide.rules"
udevadm control --reload
ok "installed and reloaded"

step "systemd units"
install -m 0644 "$HERE/polyseat-uhid-observer.service" "$UNITDIR/"
install -m 0644 "$HERE/polyseat-broker@.service" "$UNITDIR/"
systemctl daemon-reload
ok "registered"

cat <<EOF

Installed. Nothing is running yet.

  systemctl enable --now polyseat-uhid-observer.service
  systemctl enable --now polyseat-broker@<seat>.service      # one per seat

The seats themselves are not created here. Until the daemon exists, use the
scripts under spike/m1-seat/. Check the host afterwards with:

  $HERE/check-hardening.sh
EOF
