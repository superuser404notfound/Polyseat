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

# Sourced only so that one line below can ask which package owns this file, and
# so missing it is a degraded report rather than no report: this script runs
# without root and its whole job is to tell somebody what it found.
_here="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
for _d in "$_here" /usr/local/lib/polyseat /usr/lib/polyseat; do
    if [[ -r $_d/distro.sh ]]; then
        # shellcheck source=host/distro.sh
        . "$_d/distro.sh"
        distro_detect || true
        break
    fi
done

declare -f pkg_owns_file >/dev/null 2>&1 || pkg_owns_file() { return 1; }

# A prefix for every sysctl directory below, so host/test-hardening.sh can hand
# it a tree of its own. Empty on a real machine.
SYSCTL_ROOT=${POLYSEAT_SYSCTL_ROOT:-}

# The only kernel.sysrq values that give a virtual keyboard nothing worth having:
# 0 is off, 16 is sync, 2 is the console log level. 1 is not a bit but "all of
# them", and every other bit can kill, reboot, remount or dump.
SYSRQ_SAFE=16

sysrq_harmless() {
    local v=$1
    [[ $v =~ ^[0-9]+$ ]] || return 1
    ((v != 1 && (v & ~(2 | 16)) == 0))
}

# sysrq_fix_value is what --fix pins, given the value running now.
#
# The value running now only when that one is harmless. Pinning whatever was
# found made --fix write kernel.sysrq = 1 into /etc on a machine that had it,
# which is the exposure this script reports, made permanent by the option
# meant to close it.
sysrq_fix_value() {
    if sysrq_harmless "$1"; then
        printf '%s\n' "$1"
    else
        printf '%s\n' "$SYSRQ_SAFE"
    fi
}

# sysrq_configured prints the kernel.sysrq value boot will apply, a tab, and
# the file that sets it, or nothing when no file does.
#
# Read the way systemd-sysctl reads it, because "some file in /etc mentions
# kernel.sysrq" was both too loose and too narrow. Too loose because the match
# was a grep, which took a commented line as a setting. Too narrow because the
# files in /usr/lib/sysctl.d count as well: systemd ships kernel.sysrq = 16 in
# 50-default.conf there, and a package file named after ours would override
# ours, since all four directories are sorted together by file name and the
# last setting wins. /etc, then /run, then /usr/local/lib, then /usr/lib decide
# only between files of the same name. /etc/sysctl.conf comes last, as procps
# reads it.
sysrq_configured() {
    local -A seen=()
    local dir f base line value='' from=''
    local names=() files=()

    for dir in /etc/sysctl.d /run/sysctl.d /usr/local/lib/sysctl.d /usr/lib/sysctl.d; do
        for f in "$SYSCTL_ROOT$dir"/*.conf; do
            [[ -e $f ]] || continue
            base=${f##*/}
            [[ -n ${seen[$base]:-} ]] && continue
            seen[$base]=$f
            names+=("$base")
        done
    done

    if ((${#names[@]})); then
        while IFS= read -r base; do
            files+=("${seen[$base]}")
        done < <(printf '%s\n' "${names[@]}" | LC_ALL=C sort)
    fi

    files+=("$SYSCTL_ROOT/etc/sysctl.conf")

    for f in "${files[@]}"; do
        [[ -r $f ]] || continue
        while IFS= read -r line || [[ -n $line ]]; do
            # A leading "-" only means "ignore a failure", so it still sets.
            if [[ $line =~ ^[[:space:]]*-?kernel[./]sysrq[[:space:]]*=[[:space:]]*([^[:space:]#\;]*) ]]; then
                value=${BASH_REMATCH[1]}
                from=$f
            fi
        done < "$f"
    done

    [[ -n $from ]] && printf '%s\t%s\n' "$value" "$from"

    return 0
}

# Sourced by host/test-hardening.sh for the functions above and nothing else.
(return 0 2>/dev/null) && return 0

step "udev rule"
# Four directories, in the order udev reads them, because two installers put the
# rule in two of them: host/install.sh writes /etc, where a local
# administrator's rules go, and the package owns /usr/lib, where a
# distribution's do. Looking only in /etc told every package install that the
# rule protecting it was missing while it was in place and working, and a
# hardening check that cries wolf is worse than none.
rule_found=
for dir in /etc/udev/rules.d /run/udev/rules.d /usr/local/lib/udev/rules.d /usr/lib/udev/rules.d; do
    if [[ -e $dir/72-polyseat-hide.rules ]]; then
        rule_found=$dir/72-polyseat-hide.rules
        break
    fi
done

if [[ -n $rule_found ]]; then
    ok "installed: $rule_found"
else
    bad "missing: seat devices would be readable on the host desktop"
    # Named by how Polyseat got here, because this script now ships in the
    # package and telling somebody to copy a file out of a checkout they do not
    # have is an instruction that cannot be followed.
    if pkg_owns_file "$0"; then
        echo "     Installing the release package again places it, and that is enough."
    else
        echo "     sudo host/install.sh   places it, from the checkout this came from"
    fi
fi

# The number is not cosmetic. At 70 it sorted before 70-uaccess.rules, which
# put the tag it had just stripped straight back on, so a copy left behind at
# the old name is a rule that runs and achieves nothing.
if [[ -e /etc/udev/rules.d/70-polyseat-hide.rules ]]; then
    bad "an old copy is still installed as 70-polyseat-hide.rules, where it loses to 70-uaccess.rules"
    echo "     sudo rm /etc/udev/rules.d/70-polyseat-hide.rules && sudo udevadm control --reload"
fi

# The rule calls a helper by absolute path, and a path that is not there is not
# a failure anybody sees: IMPORT{program} sets nothing, the structural gate that
# follows it never fires, and the report still looks healthy because the name
# patterns underneath catch every ordinary device. That is not a hypothesis.
# This file named /usr/local only, which is where install.sh puts the helpers,
# while the package puts them in /usr/lib, so on every packaged install the
# structural half had never run once. It surfaced when a seat's PS5 pad turned
# up readable on the host: Sunshine emulates a DualSense and calls it "Wireless
# Controller", which is what a real one is called, so no name pattern can ever
# claim that device and only the structural answer can.
if [[ -n $rule_found ]]; then
    helper_found=
    while read -r helper; do
        if [[ -x $helper ]]; then
            helper_found=$helper
            break
        fi
    done < <(grep -o 'IMPORT{program}="[^" ]*' "$rule_found" | cut -d'"' -f2)

    if [[ -n $helper_found ]]; then
        ok "structural check runs $helper_found"
    else
        bad "the rule names a helper that is not installed, so a seat's gamepad is hidden by name alone"
        echo "     Sunshine names an emulated DualSense the same as a real one, and"
        echo "     no name pattern can tell those apart. Reinstalling Polyseat places"
        echo "     the helper the rule looks for."
    fi
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

if ((leaky)); then
    # Learned the slow way, and worth the three lines: fixing the rule does not
    # close a device that is already open. A reload plus a trigger left a
    # leaking PS5 pad exactly as it was, and only unplugging it helped. The
    # likely reason - not proven here - is that the rules did run again and
    # correctly stripped the tag, while the entry logind had already granted
    # stays: a rule that no longer asks for uaccess is not the same as something
    # asking for it to be taken back. Either way the entry goes when the device
    # does, so the instruction is the same.
    echo "     Reloading the rule does not close a device that is already open."
    echo "     Unplug and replug the controller, or restart the seat's session, so"
    echo "     the device is created again under the rule now in place."
else
    ok "all virtual input and raw HID devices are root:root 0600 with no ACL"
fi

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

configured=$(sysrq_configured)
cvalue=${configured%%$'\t'*}
cfile=${configured#*$'\t'}
pinned=0

if [[ -z $configured ]]; then
    warn "not pinned: no sysctl file sets it, so a distribution default may change it"
elif [[ $cfile == "$SYSCTL_ROOT"/usr/* ]]; then
    warn "set to $cvalue by $cfile, a distribution's file that an update may change"
elif sysrq_harmless "$cvalue"; then
    ok "pinned at $cvalue in $cfile, cannot drift on an update"
    pinned=1
else
    bad "pinned at $cvalue in $cfile, which lets a seat's keyboard do more than sync"
fi

if ((FIX)) && { ((!pinned)) || ! sysrq_harmless "$sysrq"; }; then
    want=$(sysrq_fix_value "$sysrq")

    ours=/etc/sysctl.d/99-polyseat-sysrq.conf
    printf '# polyseat: virtual keyboards from the seats are attached to the\n# kernel sysrq handler, so pin this rather than letting a distribution\n# default drift. 16 permits sync only.\nkernel.sysrq = %s\n' "$want" \
        > "$SYSCTL_ROOT$ours"
    sysctl --system >/dev/null 2>&1

    # Written is not the same as winning. A file that sorts after ours sets the
    # value last, and saying "pinned" then would be the check crying wolf the
    # other way round.
    configured=$(sysrq_configured)
    if [[ ${configured#*$'\t'} == "$SYSCTL_ROOT$ours" ]]; then
        ok "pinned at $want in $ours"
    else
        bad "wrote $ours, but ${configured#*$'\t'} sorts after it and still sets ${configured%%$'\t'*}"
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
