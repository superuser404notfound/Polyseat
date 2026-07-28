#!/usr/bin/env bash
# Reports host-side exposures around seat input devices.
#
# These are the ones no seat-side measure can close, because the devices are
# created in the host kernel and are attached to its handlers like any physical
# keyboard.
#
#   ./check-hardening.sh          report only
#   sudo ./check-hardening.sh --fix   additionally pin kernel.sysrq
set -uo pipefail

ok()   { printf '  \033[32m✓\033[0m %s\n' "$*"; }
bad()  { printf '  \033[31m✗\033[0m %s\n' "$*"; }
warn() { printf '  \033[33m!\033[0m %s\n' "$*"; }
step() { printf '\n\033[1m%s\033[0m\n' "$*"; }

FIX=0
[[ "${1:-}" == "--fix" ]] && FIX=1

step "udev rule"
if [[ -e /etc/udev/rules.d/70-polyseat-hide.rules ]]; then
    ok "installed"
else
    bad "missing: seat devices would be readable on the host desktop"
    echo "     sudo cp host/70-polyseat-hide.rules /etc/udev/rules.d/ && sudo udevadm control --reload"
fi

step "Virtual devices readable by the desktop user?"
leaky=0
for d in /sys/class/input/event*; do
    [[ -r "$d/device/name" ]] || continue
    [[ "$(readlink -f "$d")" == */devices/virtual/* ]] || continue
    node="/dev/input/${d##*/}"
    perms=$(stat -c '%U:%G %a' "$node" 2>/dev/null)
    if [[ "$perms" != "root:root 600" ]]; then
        bad "$node is $perms  ($(<"$d/device/name"))"
        leaky=1
    fi
done
((leaky)) || ok "all virtual devices are root:root 0600"

step "SysRq"
# Virtual keyboards are attached to the sysrq handler, so a client can send
# SysRq combinations. The bitmask decides what those can do.
sysrq=$(cat /proc/sys/kernel/sysrq)
case "$sysrq" in
    0)  ok "kernel.sysrq = 0, disabled entirely" ;;
    16) ok "kernel.sysrq = 16, sync only, harmless" ;;
    1)  bad "kernel.sysrq = 1, everything allowed including reboot and crash" ;;
    *)  warn "kernel.sysrq = $sysrq, check the bitmask" ;;
esac
if grep -rqs 'kernel.sysrq' /etc/sysctl.conf /etc/sysctl.d/ 2>/dev/null; then
    ok "pinned in sysctl.d, cannot drift on an update"
else
    warn "not pinned: a distribution default may change it"
    if ((FIX)); then
        printf '# polyseat: virtual keyboards from the seats are attached to the\n# kernel sysrq handler, so pin this rather than letting a distribution\n# default drift. 16 permits sync only.\nkernel.sysrq = %s\n' "$sysrq" \
            > /etc/sysctl.d/99-polyseat-sysrq.conf
        sysctl --system >/dev/null 2>&1
        ok "pinned at $sysrq in /etc/sysctl.d/99-polyseat-sysrq.conf"
    fi
fi

step "Virtual terminals"
# /sys is readable without root, fgconsole is not.
active=$(sed 's/tty//' /sys/class/tty/tty0/active 2>/dev/null || echo "?")
echo "  active VT: $active"
for t in 1 2 3 4 5 6; do
    [[ -e "/dev/tty$t" ]] || continue
    mode=$(kbd_mode -C "/dev/tty$t" 2>/dev/null)
    case "$mode" in
        *isabled*|*eaktivier*) state="K_OFF, ignores the keyboard" ;;
        *) state="ACTIVE, would accept keystrokes" ;;
    esac
    marker=" "; [[ "$t" == "$active" ]] && marker="*"
    printf "   %s tty%s: %s\n" "$marker" "$t" "$state"
done
if systemctl is-enabled getty@.service >/dev/null 2>&1; then
    warn "getty@.service is enabled: switching to a free VT opens a login prompt"
else
    ok "no getty on the free VTs"
fi

step "Is the window open right now?"
# The exposure only materialises while a text console is the active VT and a
# seat holds input devices. That combination is worth calling out when it
# actually happens, rather than only describing it.
graphical_vt=$(loginctl list-sessions --no-legend 2>/dev/null \
    | awk '{print $1}' \
    | while read -r sid; do
          [ -n "$sid" ] || continue
          if [ "$(loginctl show-session "$sid" -p Type --value 2>/dev/null)" != tty ]; then
              loginctl show-session "$sid" -p VTNr --value 2>/dev/null
          fi
      done | head -1)
attached=0
for d in /sys/class/input/event*; do
    [ -r "$d/device/name" ] || continue
    [[ "$(readlink -f "$d")" == */devices/virtual/* ]] || continue
    attached=1
    break
done
if [[ "$attached" == 0 ]]; then
    ok "no seat devices exist at the moment, nothing to expose"
elif [[ -n "$graphical_vt" && "$active" == "$graphical_vt" ]]; then
    ok "the graphical session holds the active VT (tty$active), console is not reachable"
else
    bad "active VT is tty$active while seat devices exist: a client types along here"
fi

cat <<'EOF'

  The window that stays open: while the desktop holds the active VT its K_OFF
  mode also blocks VT switching, so a client cannot reach a console by itself.
  If you switch to a text console by hand while a seat is streaming, that
  client types along.

  Setting the free VTs to K_OFF would close it and is deliberately not done
  here: it disables your real keyboard on those consoles too, and with it the
  recovery path you want when the desktop is broken.
EOF
