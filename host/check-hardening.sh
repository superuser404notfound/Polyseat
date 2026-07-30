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
if [[ -e /etc/udev/rules.d/72-polyseat-hide.rules ]]; then
    ok "installed"
else
    bad "missing: seat devices would be readable on the host desktop"
    echo "     sudo cp host/72-polyseat-hide.rules /etc/udev/rules.d/ && sudo udevadm control --reload"
fi

# The number is not cosmetic. At 70 it sorted before 70-uaccess.rules, which
# put the tag it had just stripped straight back on, so a copy left behind at
# the old name is a rule that runs and achieves nothing.
if [[ -e /etc/udev/rules.d/70-polyseat-hide.rules ]]; then
    bad "an old copy is still installed as 70-polyseat-hide.rules, where it loses to 70-uaccess.rules"
    echo "     sudo rm /etc/udev/rules.d/70-polyseat-hide.rules && sudo udevadm control --reload"
fi

step "Virtual devices readable by the desktop user?"
leaky=0

# Permissions and the access control list, because they are two different
# answers. logind grants the desktop user an entry through the uaccess tag and
# that entry survives a mode change, so a node can read root:root 0600 and
# still be open to somebody. Reading only the mode is how a controller leaking
# to the host went unnoticed.
check_node() {
    local node=$1 what=$2
    local perms
    perms=$(stat -c '%U:%G %a' "$node" 2>/dev/null) || return

    if [[ "$perms" != "root:root 600" ]]; then
        bad "$node is $perms  ($what)"
        leaky=1
    fi

    if getfacl -p "$node" 2>/dev/null | grep -qE '^user:[^:]+:'; then
        bad "$node has an access control entry for a user  ($what)"
        leaky=1
    fi
}

for d in /sys/class/input/event* /sys/class/input/js*; do
    [[ -r "$d/device/name" ]] || continue
    [[ "$(readlink -f "$d")" == */devices/virtual/* ]] || continue
    check_node "/dev/input/${d##*/}" "$(<"$d/device/name")"
done

# The other half of a gamepad. A uhid device appears both under /dev/input and
# as /dev/hidrawN, and hidraw is the one Steam reads a DualSense through. It
# was missing from this check for as long as it was missing from the rule.
for h in /sys/class/hidraw/hidraw*; do
    [[ -e "$h" ]] || continue
    [[ "$(readlink -f "$h")" == */devices/virtual/* ]] || continue
    name=$(grep -m1 '^HID_NAME=' "$h/device/uevent" 2>/dev/null | cut -d= -f2-)
    check_node "/dev/${h##*/}" "${name:-unnamed}"
done

((leaky)) || ok "all virtual input and raw HID devices are root:root 0600 with no ACL"

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
#
# Which VTs a graphical login holds. "Not a text session" was too loose a test
# for that, and it picked the wrong session on two counts. The class rules out
# a display manager's greeter, which under GDM keeps a session of its own on
# another VT for as long as the machine runs, so the check compared the active
# VT against the login screen's and called a perfectly normal desktop exposed.
# SDDM hides that by ending its greeter at login, which is why it never showed
# up here. A VT number of zero rules out sessions that have no console at all,
# such as the "manager" session systemd opens for the user; that one is on this
# very machine and stayed out of the way only by sorting last.
#
# Every match is kept rather than the first, because fast user switching gives
# two graphical sessions on two VTs and either one holding the active VT means
# no console is reachable.
graphical_vts=$(loginctl list-sessions --no-legend 2>/dev/null \
    | awk '{print $1}' \
    | while read -r sid; do
          [ -n "$sid" ] || continue
          sclass=$(loginctl show-session "$sid" -p Class --value 2>/dev/null)
          stype=$(loginctl show-session "$sid" -p Type --value 2>/dev/null)
          svt=$(loginctl show-session "$sid" -p VTNr --value 2>/dev/null)
          [ "$sclass" = user ] || continue
          case "$stype" in wayland|x11|mir) ;; *) continue ;; esac
          [ -n "$svt" ] && [ "$svt" != 0 ] && echo "$svt"
      done)
attached=0
for d in /sys/class/input/event*; do
    [ -r "$d/device/name" ] || continue
    [[ "$(readlink -f "$d")" == */devices/virtual/* ]] || continue
    attached=1
    break
done
if [[ "$attached" == 0 ]]; then
    ok "no seat devices exist at the moment, nothing to expose"
elif [[ -n "$graphical_vts" ]] && grep -qxF "$active" <<<"$graphical_vts"; then
    ok "a graphical session holds the active VT (tty$active), console is not reachable"
elif [[ -z "$graphical_vts" ]]; then
    bad "nobody is logged in graphically while seat devices exist: tty$active is a console a client types along on"
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
