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
#   sudo ./lan-bridge.sh          make the uplink a bridge, move the seats onto it
#   sudo ./lan-bridge.sh --undo   put both back the way they were
#   sudo ./lan-bridge.sh --check  say what is in place now and change nothing
#
# The seats are stopped for the duration and started again at the end, because
# the kernel refuses to make an interface a bridge port while anything holds a
# macvlan on it. That refusal is EBUSY from the enslave and it does not care
# that the macvlan lives in a container's own network namespace. The first
# version of this script did not know that, went ahead anyway and left the
# machine with the address on neither interface for as long as it took somebody
# to notice.
#
# NetworkManager only, which is what the supported distributions use. The
# machine is briefly off the network while the address moves, so run it from a
# keyboard attached to the machine and not over the connection it is changing.
# Every step that can fail puts everything back, including the seats.
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

command -v incus >/dev/null || {
    bad "incus is not installed, so the seats cannot be moved with the network"
    exit 1
}

# The interface carrying the default route, which is the one the seats hang off.
# Read the same way the daemon reads it, so the two cannot disagree.
uplink() {
    awk '$2 == "00000000" { print $1; exit }' /proc/net/route
}

is_bridge() { [[ -d /sys/class/net/$1/bridge ]]; }

# seats_on lists every instance with a NIC of the given kind hanging off the
# given parent, running or not.
#
# Asked of Incus rather than of the kernel, because a container's macvlan lives
# in its own network namespace: it does not appear under the uplink's upper_*
# entries and nothing in /sys/class/net on the host mentions it. It still counts
# for the enslave, which is exactly the trap that made this necessary.
seats_on() {
    local kind=$1 parent=$2

    incus list --format csv -c n 2>/dev/null | while IFS= read -r name; do
        [[ -n $name ]] || continue

        if incus config device get "$name" eth1 nictype 2>/dev/null | grep -qx "$kind" &&
           incus config device get "$name" eth1 parent 2>/dev/null | grep -qx "$parent"; then
            echo "$name"
        fi
    done
}

running() { [[ $(incus info "$1" 2>/dev/null | awk '$1 == "Status:" { print tolower($2) }') == running ]]; }

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

    step "Seats"

    for kind in macvlan bridged; do
        list=$(seats_on "$kind" "$UPLINK" | paste -sd' ')
        [[ -n $list ]] && ok "$kind on $UPLINK: $list"
    done

    list=$(seats_on bridged "$BRIDGE" | paste -sd' ')
    [[ -n $list ]] && ok "bridged on $BRIDGE: $list"

    exit 0
fi

# --------------------------------------------------------------- seat handling

STOPPED=()

stop_seats() {
    local seats=("$@") name

    for name in "${seats[@]}"; do
        if running "$name"; then
            # A timeout, because incus stop is known to wait on a container that
            # is already gone. Everything here happens before the network is
            # touched, so a seat that will not stop costs nothing but the
            # attempt.
            incus stop --timeout 90 "$name" >/dev/null 2>&1 || {
                bad "$name would not stop, so nothing has been changed"
                start_seats
                exit 1
            }

            STOPPED+=("$name")
            ok "$name stopped"
        fi
    done
}

start_seats() {
    local name

    for name in "${STOPPED[@]:-}"; do
        [[ -n $name ]] || continue
        incus start "$name" >/dev/null 2>&1 && ok "$name started again" ||
            warn "$name did not start again, start it from the interface"
    done

    STOPPED=()
}

# repoint moves a seat's LAN interface between the two arrangements. Left alone,
# a seat built for one would come up with a NIC pointing at something that is no
# longer there.
repoint() {
    local name=$1 kind=$2 parent=$3

    incus config device set "$name" eth1 nictype "$kind" parent "$parent" >/dev/null
    ok "$name now takes its LAN interface as $kind from $parent"
}

# ------------------------------------------------------------------- undo

if [[ $MODE == undo ]]; then
    step "Removing the bridge"

    if ! nmcli -t -f NAME con show | grep -qx "$BRIDGE_CON"; then
        warn "no $BRIDGE_CON connection, so there is nothing of ours to remove"
        exit 0
    fi

    PORT=$(ls /sys/class/net/"$BRIDGE"/brif 2>/dev/null | head -1)

    if [[ -z $PORT ]]; then
        bad "$BRIDGE has no port, so there is no interface to give the address back to"
        exit 1
    fi

    ok "$PORT is the port to hand back to"

    mapfile -t SEATS < <(seats_on bridged "$BRIDGE")

    if ((${#SEATS[@]})); then
        step "Stopping the seats"
        stop_seats "${SEATS[@]}"
    fi

    step "Putting the address back on $PORT"

    original=$(nmcli -t -f NAME,TYPE con show | awk -F: '$2 == "802-3-ethernet" { print $1 }' \
        | grep -vx "$PORT_CON" | head -1)

    nmcli con delete "$PORT_CON" >/dev/null 2>&1 || true
    nmcli con delete "$BRIDGE_CON" >/dev/null 2>&1 || true

    if [[ -n $original ]]; then
        nmcli con mod "$original" connection.autoconnect yes
    else
        warn "no original wired connection left to restore, making one"
        original="wired-$PORT"
        nmcli con add type ethernet ifname "$PORT" con-name "$original" \
            ipv4.method auto ipv6.method auto connection.autoconnect yes >/dev/null
    fi

    nmcli con up "$original" >/dev/null
    ok "$original is up"

    if ((${#SEATS[@]})); then
        step "Putting the seats back on macvlan"

        for name in "${SEATS[@]}"; do
            repoint "$name" macvlan "$PORT"
        done

        start_seats
    fi

    exit 0
fi

# ------------------------------------------------------------------ install

step "Uplink"

if is_bridge "$UPLINK"; then
    ok "$UPLINK is already a bridge"

    mapfile -t SEATS < <(seats_on macvlan "$UPLINK")

    if ((${#SEATS[@]} == 0)); then
        ok "and every seat is already on it, nothing to do"
        exit 0
    fi

    step "Moving the seats onto it"
    stop_seats "${SEATS[@]}"

    for name in "${SEATS[@]}"; do
        repoint "$name" bridged "$UPLINK"
    done

    start_seats
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

# ---- the seats have to let go of the interface before it can become a port

mapfile -t SEATS < <(seats_on macvlan "$UPLINK")

if ((${#SEATS[@]})); then
    step "Stopping the seats"
    echo "    The kernel refuses to make an interface a bridge port while anything"
    echo "    holds a macvlan on it, and a seat's macvlan counts even though it"
    echo "    lives in the seat's own network namespace."

    stop_seats "${SEATS[@]}"
fi

# ---- from here on, anything that fails puts everything back

rollback() {
    bad "$1"
    warn "putting everything back"

    nmcli con delete "$PORT_CON" >/dev/null 2>&1 || true
    nmcli con delete "$BRIDGE_CON" >/dev/null 2>&1 || true

    nmcli con mod "$CURRENT" connection.autoconnect yes >/dev/null 2>&1 || true

    if nmcli con up "$CURRENT" >/dev/null 2>&1; then
        ok "$CURRENT is up again"
    else
        bad "$CURRENT did not come up, the machine may be off the network"
        echo "    By hand: nmcli con up \"$CURRENT\""
    fi

    start_seats
    exit 1
}

step "Building the bridge"

# The bridge takes the interface's own MAC address. Whatever hands out addresses
# on this network knows the machine by that, and a reservation or a firewall
# rule that names it has to keep working across this change: the point is to add
# the seats to the segment, not to make the host a different machine on it.
MAC=$(cat /sys/class/net/"$UPLINK"/address)

nmcli con add type bridge ifname "$BRIDGE" con-name "$BRIDGE_CON" \
    bridge.stp no \
    bridge.forward-delay 0 \
    bridge.mac-address "$MAC" \
    ipv4.method auto \
    ipv6.method auto \
    connection.autoconnect yes >/dev/null || rollback "the bridge connection could not be created"

ok "$BRIDGE created with $UPLINK's address $MAC"

nmcli con add type ethernet ifname "$UPLINK" con-name "$PORT_CON" \
    controller "$BRIDGE_CON" \
    port-type bridge \
    connection.autoconnect yes >/dev/null || rollback "the port connection could not be created"

ok "$UPLINK prepared as its port"

step "Moving the address over"

# The old connection is stopped from coming back before anything is taken down.
# Left on autoconnect it would race the port connection for the interface on the
# next boot, and which one won would decide whether the machine had a network.
nmcli con mod "$CURRENT" connection.autoconnect no
nmcli con down "$CURRENT" >/dev/null 2>&1 || true

nmcli con up "$BRIDGE_CON" >/dev/null 2>&1 ||
    rollback "$BRIDGE did not come up"

# The port is the step that failed the first time and it failed quietly. It is
# checked, and a failure here is as fatal as the bridge failing: a bridge with
# no port is a machine with no network.
nmcli con up "$PORT_CON" >/dev/null 2>&1 ||
    rollback "$UPLINK could not be made a port of $BRIDGE"

[[ -e /sys/class/net/$BRIDGE/brif/$UPLINK ]] ||
    rollback "$UPLINK is not a port of $BRIDGE even though the connection came up"

ok "$UPLINK is a port of $BRIDGE"

# An address, not merely a link. Everything above can succeed and still leave a
# bridge that never got a lease, and reporting success then would be reporting
# the opposite of what happened.
for _ in $(seq 30); do
    ADDRESS=$(ip -4 -br addr show "$BRIDGE" | awk '{ print $3 }')
    [[ -n $ADDRESS ]] && break
    sleep 1
done

[[ -n ${ADDRESS:-} ]] || rollback "$BRIDGE came up but never got an address"

ok "$BRIDGE holds $ADDRESS"

GATEWAY=$(ip -4 route show default dev "$BRIDGE" | awk '{ print $3; exit }')

if [[ -n $GATEWAY ]] && ping -c1 -W2 "$GATEWAY" >/dev/null 2>&1; then
    ok "the gateway $GATEWAY answers over $BRIDGE"
else
    rollback "nothing answers over $BRIDGE, so the machine is not really on the network"
fi

# ---- the seats come back on the bridge

if ((${#SEATS[@]})); then
    step "Moving the seats onto the bridge"

    for name in "${SEATS[@]}"; do
        repoint "$name" bridged "$BRIDGE"
    done

    start_seats
fi

step "Done"
echo "    The host and the seats are on one segment now. The daemon reads what"
echo "    the uplink is, so seats provisioned from here get a bridged NIC by"
echo "    themselves. sudo $0 --undo puts all of it back."
