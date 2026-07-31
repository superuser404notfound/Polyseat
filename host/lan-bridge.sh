#!/usr/bin/env bash
# Puts the host and its seats on the same network segment.
#
# By default a seat reaches the LAN through a macvlan on the wired interface.
# That gives every seat its own address and its own Sunshine ports for nothing,
# and it carries one property that is not a setting anybody can change: a
# macvlan interface and the interface it hangs off cannot talk to each other.
# The host and the seats are on the same wire and cannot see each other on it.
#
# For streaming that never mattered. For sitting at this machine and playing a
# local multiplayer game against somebody in a seat it is fatal, because that is
# not one missing route, it is two ends of the same cable that the kernel keeps
# apart. Games find each other by broadcasting on the network they are on, and
# there is nothing to broadcast to.
#
# The fix is to make the wired interface a port on a bridge and give the host
# its address on the bridge instead. Then the host is a device on that segment
# like any other, the seats become ports on the same bridge, and everything that
# works between two machines on a switch works between the host and a seat:
# broadcast discovery, direct connections, Steam's own local network transfer.
#
#   sudo ./lan-bridge.sh          make the uplink a bridge
#   sudo ./lan-bridge.sh --undo   put it back the way it was
#   sudo ./lan-bridge.sh --check  say what is in place now and change nothing
#
# NetworkManager only, which is what the supported distributions use. The
# machine is briefly off the network while the address moves, so run it from a
# keyboard attached to the machine and not over the connection it is changing.
#
# What this costs is written down in docs/security.md and is worth reading
# first: a seat that can reach the host over the LAN can reach the services the
# host is listening on.
set -euo pipefail

BRIDGE=br0
BRIDGE_CON=polyseat-bridge
PORT_CON=polyseat-uplink

ok()   { printf '  \033[32m✓\033[0m %s\n' "$*"; }
bad()  { printf '  \033[31m✗\033[0m %s\n' "$*"; }
warn() { printf '  \033[33m!\033[0m %s\n' "$*"; }
step() { printf '\n\033[1m%s\033[0m\n' "$*"; }

MODE=install
for arg in "$@"; do
    case "$arg" in
        --undo)  MODE=undo ;;
        --check) MODE=check ;;
        *) echo "unknown argument: $arg" >&2; exit 1 ;;
    esac
done

[[ $EUID -eq 0 ]] || { echo "needs root"; exit 1; }

command -v nmcli >/dev/null || {
    bad "nmcli is not installed, so this script cannot configure the network here"
    echo "    The same thing by hand: create a bridge, make the wired interface"
    echo "    its only port, and move the address configuration onto the bridge."
    exit 1
}

# The interface carrying the default route, which is the one the seats hang off.
# Read the same way the daemon reads it, so the two cannot disagree.
uplink() {
    awk '$2 == "00000000" { print $1; exit }' /proc/net/route
}

is_bridge() { [[ -d /sys/class/net/$1/bridge ]]; }

# ------------------------------------------------------------------- check

UPLINK="$(uplink || true)"

if [[ -z $UPLINK ]]; then
    bad "no default route, so there is no uplink to work on"
    exit 1
fi

if [[ $MODE == check ]]; then
    step "Uplink"

    if is_bridge "$UPLINK"; then
        ports=$(ls /sys/class/net/"$UPLINK"/brif 2>/dev/null | paste -sd' ')
        ok "$UPLINK is a bridge, ports: ${ports:-none}"
        echo "    The host and the seats are on the same segment and can see"
        echo "    each other. Seats provisioned from here get a bridged NIC."
    else
        ok "$UPLINK is a plain interface"
        echo "    Seats get a macvlan from it, so each seat is its own device on"
        echo "    the LAN and the host cannot see any of them on it."
    fi

    exit 0
fi

# ------------------------------------------------------------------- undo

if [[ $MODE == undo ]]; then
    step "Removing the bridge"

    if ! nmcli -t -f NAME con show | grep -qx "$BRIDGE_CON"; then
        warn "no $BRIDGE_CON connection, so there is nothing of ours to remove"
        exit 0
    fi

    # The interface's own connection is brought back first and the bridge taken
    # down after, so the machine is never left with neither.
    original=$(nmcli -t -f NAME,TYPE con show | awk -F: '$2 == "802-3-ethernet" { print $1 }' \
        | grep -vx "$PORT_CON" | head -1)

    if [[ -n $original ]]; then
        nmcli con mod "$original" connection.autoconnect yes
        ok "$original will come up on its own again"
    else
        warn "no original wired connection left to restore, making one"
        device=$(ls /sys/class/net/"$BRIDGE"/brif 2>/dev/null | head -1)
        nmcli con add type ethernet ifname "${device:-eth0}" con-name "wired-$device" \
            ipv4.method auto ipv6.method auto >/dev/null
        original="wired-$device"
    fi

    nmcli con delete "$PORT_CON" >/dev/null 2>&1 || true
    nmcli con delete "$BRIDGE_CON" >/dev/null 2>&1 || true
    ok "the bridge connections are gone"

    nmcli con up "$original" >/dev/null
    ok "$original is up"

    step "Now reprovision the seats"
    echo "    They still have a bridged NIC pointing at a bridge that no longer"
    echo "    exists. Provisioning gives them a macvlan again."

    exit 0
fi

# ------------------------------------------------------------------ install

step "Uplink"

if is_bridge "$UPLINK"; then
    ok "$UPLINK is already a bridge, nothing to do"
    echo "    Reprovision the seats if they were built before it was."
    exit 0
fi

ok "$UPLINK carries the default route"

if [[ -d /sys/class/net/$UPLINK/wireless ]] || [[ -e /sys/class/net/$UPLINK/phy80211 ]]; then
    bad "$UPLINK is wireless and cannot be bridged"
    echo "    802.11 does not carry more than one MAC address per station, which"
    echo "    is the same reason macvlan does not work on it either. Seats need a"
    echo "    wired interface."
    exit 1
fi

CURRENT=$(nmcli -t -f NAME,DEVICE con show --active | awk -F: -v d="$UPLINK" '$2 == d { print $1; exit }')

if [[ -z $CURRENT ]]; then
    bad "NetworkManager is not managing $UPLINK, so this script would not know what to move"
    exit 1
fi

ok "NetworkManager has it as \"$CURRENT\""

step "Building the bridge"

# The bridge takes the interface's own MAC address. Whatever hands out addresses
# on this network knows the machine by that, and a reservation or a firewall
# rule that names it has to keep working across this change: the point is to add
# the seats to the segment, not to make the host a different machine on it.
MAC=$(cat /sys/class/net/"$UPLINK"/address)

nmcli con add type bridge ifname "$BRIDGE" con-name "$BRIDGE_CON" \
    bridge.stp no \
    bridge.forward-delay 0 \
    ethernet.cloned-mac-address "$MAC" \
    ipv4.method auto \
    ipv6.method auto \
    connection.autoconnect yes >/dev/null

ok "$BRIDGE created with $UPLINK's address $MAC"

nmcli con add type ethernet ifname "$UPLINK" con-name "$PORT_CON" \
    master "$BRIDGE" \
    connection.autoconnect yes >/dev/null

ok "$UPLINK added as its port"

step "Moving the address over"

# The old connection is stopped from coming back before anything is taken down.
# Left on autoconnect it would race the port connection for the interface on the
# next boot, and which one won would decide whether the machine had a network.
nmcli con mod "$CURRENT" connection.autoconnect no
nmcli con down "$CURRENT" >/dev/null 2>&1 || true

if ! nmcli con up "$BRIDGE_CON" >/dev/null; then
    bad "the bridge did not come up, putting $CURRENT back"
    nmcli con mod "$CURRENT" connection.autoconnect yes
    nmcli con up "$CURRENT" >/dev/null || true
    nmcli con delete "$PORT_CON" >/dev/null 2>&1 || true
    nmcli con delete "$BRIDGE_CON" >/dev/null 2>&1 || true
    exit 1
fi

nmcli con up "$PORT_CON" >/dev/null || true

address=$(ip -4 -br addr show "$BRIDGE" | awk '{ print $3 }')

if [[ -z $address ]]; then
    warn "$BRIDGE is up but has no address yet, give DHCP a moment"
else
    ok "$BRIDGE holds $address"
fi

step "Now reprovision the seats"
echo "    The daemon reads what the uplink is and gives a seat a bridged NIC"
echo "    when it is a bridge, so this takes effect on the next provisioning run"
echo "    and not before. Until then the seats are still on macvlan and still"
echo "    cannot see this machine."
