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
# One thing it changes outside the bridge: the uplink ends up with a saved
# profile of its own, because it very likely had none. NetworkManager invents a
# connection for an ethernet device with no profile, keeps it in /run and builds
# it again at every boot, and a bridge cannot be put underneath something that
# comes back every time. Deleting it is the supported way to stop that, and
# --undo writes a real profile in its place rather than waiting for the invented
# one to return.
#
# What this costs is written down in docs/security.md and is worth reading
# first: a seat that can reach the host over the LAN can reach the services the
# host is listening on.
set -euo pipefail

# Everything below reads nmcli, and nmcli translates. On a German desktop
# connection.autoconnect answers "ja", which no comparison here expects.
export LC_ALL=C

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

CONFIG=/etc/polyseat/polyseatd.json

# ask_daemon gets the uplink from the binary that owns the choice.
#
# There is one policy and it is written in Go: the configuration, then the
# interface carrying the default route, then a wired card with a cable in it
# when that route is wireless. This script used to work a smaller version of
# that out for itself and got it wrong on precisely the machine the last case
# exists for. Asking beats reimplementing, and the name is on stdout with the
# reason on stderr so both can be had without parsing either.
#
# It writes $UPLINK and $UPLINK_SOURCE and answers whether it managed.
ask_daemon() {
    local polyseatd why

    for polyseatd in /usr/local/bin/polyseatd /usr/bin/polyseatd; do
        [[ -x $polyseatd ]] || continue

        why=$(mktemp)
        status=0

        UPLINK=$("$polyseatd" -uplink 2>"$why") || status=$?
        UPLINK_SOURCE=$(<"$why")

        rm -f "$why"

        if ((status == 0)) && [[ -n $UPLINK ]]; then
            return 0
        fi

        # Exit 1 is this daemon saying there is no uplink, which is a fact about
        # the machine: reading it here again would only disagree with it more
        # politely. Anything else is a binary that does not know the question,
        # which an older one does not, and then working it out here is right.
        if ((status == 1)); then
            UPLINK=""

            return 0
        fi

        UPLINK=""
        UPLINK_SOURCE=""

        return 1
    done

    return 1
}

# configured_uplink reads the "uplink" key, and prints nothing when there is
# none, which is the ordinary case.
#
# With python, because that is what prepare.sh reads this file with and one JSON
# parser on the machine beats two. A file that cannot be read is reported rather
# than skipped: an uplink that is named and quietly ignored is exactly the
# disagreement this exists to end.
configured_uplink() {
    [[ -r $CONFIG ]] || return 0

    local python
    for python in python3 python; do
        command -v "$python" >/dev/null 2>&1 || continue

        "$python" -c 'import json,sys;print(json.load(open(sys.argv[1])).get("uplink") or "")' \
            "$CONFIG" 2>/dev/null

        return 0
    done

    grep -q '"uplink"' "$CONFIG" 2>/dev/null &&
        warn "$CONFIG names an uplink and there is no python here to read it with" >&2

    return 0
}

# default_route_device is the interface the machine's way out goes over, which
# is not always the one the seats hang off.
default_route_device() {
    awk '$2 == "00000000" { print $1; exit }' /proc/net/route
}

# enslaved names the interface $1 is a port of, and nothing when it is a port of
# nothing.
#
# The daemon does the same thing with a configured uplink, and both do it for
# one reason: bridging changes which name is right without changing the
# configuration that named it. A machine that says "uplink": "enp4s0" and then
# has enp4s0 made a port of br0 still says enp4s0, and every question worth
# asking from here on — is it a bridge, which seats hang off it — has to be
# asked of br0. Without this the script and the daemon disagree again, one step
# further along than they used to.
enslaved() {
    # The name comes out of a configuration file and is about to be joined onto
    # a path.
    [[ -n $1 && $1 == "${1##*/}" && $1 != "." && $1 != ".." ]] || return 0

    # readlink and not readlink -f. With -f a path whose last component does not
    # exist still resolves and prints, so every interface on the machine would
    # come back as a port of something called "master".
    local master
    master=$(readlink "/sys/class/net/$1/master" 2>/dev/null) || return 0

    [[ -n $master ]] || return 0

    basename "$master"
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

# con_file says where NetworkManager keeps a profile, which is the difference
# between a setting that survives a reboot and one that does not. A path under
# /run is not a saved profile: it is the default wired connection NetworkManager
# invents for a managed ethernet device that has none, and it is invented again
# from scratch at the next boot.
con_file() {
    nmcli -t -f UUID,FILENAME con show | awk -F: -v u="$1" '$1 == u { print $2; exit }'
}

generated() { [[ $(con_file "$1") == /run/* ]]; }

# saved_wired_for names a saved ethernet profile belonging to $1, autoconnect or
# not: on the way back it is the one that was switched off that has to be
# switched on again, and being switched off is what makes it invisible to
# claimants.
saved_wired_for() {
    local dev=$1 name uuid type ifname

    while IFS=: read -r name uuid type; do
        [[ $type == 802-3-ethernet ]] || continue
        [[ $name == "$PORT_CON" ]] && continue

        # An invented one does not count. Handing the interface back to a
        # profile that only lives in /run is how the machine ends up with
        # nothing to come up with after the next reboot, which is the whole
        # fault being undone here.
        generated "$uuid" && continue

        ifname=$(nmcli -g connection.interface-name con show "$name")
        [[ -z $ifname || $ifname == "$dev" ]] || continue

        echo "$name"
        return
    done < <(nmcli -t -f NAME,UUID,TYPE con show)
}

# claimants lists the profiles that would bring $1 up by themselves at the next
# boot: ethernet profiles with autoconnect on, bound either to that interface or
# to no interface at all. More than one of them is a race.
claimants() {
    local dev=$1 name type autoconnect ifname

    while IFS=: read -r name type autoconnect; do
        [[ $type == 802-3-ethernet ]] || continue
        [[ $autoconnect == yes ]] || continue

        ifname=$(nmcli -g connection.interface-name con show "$name")
        [[ -z $ifname || $ifname == "$dev" ]] || continue

        echo "$name"
    done < <(nmcli -t -f NAME,TYPE,AUTOCONNECT con show)
}

# ------------------------------------------------------------------- check

DEFAULT_DEV="$(default_route_device || true)"

UPLINK=""
UPLINK_SOURCE=""

# The binary first, and only when there is none does this answer the question
# itself: the configuration, then the default route.
#
# The smaller answer is what this script used to have all of, under a comment
# claiming it read the uplink "the same way the daemon reads it, so the two
# cannot disagree". They could and did, on a machine whose route out is wireless
# and whose seats hang off a wired card. A checkout that has this file and no
# polyseatd yet still gets the smaller answer, which is right for it: without a
# daemon there are no seats to disagree about.
if ! ask_daemon; then
    CONFIGURED="$(configured_uplink || true)"

    if [[ -n $CONFIGURED ]]; then
        UPLINK=$CONFIGURED
        UPLINK_SOURCE="\"uplink\" in $CONFIG"

        # Followed to its bridge when it has become a port of one, which is
        # what this script does to it and what the daemon reads afterwards.
        MASTER="$(enslaved "$CONFIGURED" || true)"

        if [[ -n $MASTER ]] && is_bridge "$MASTER"; then
            UPLINK=$MASTER
            UPLINK_SOURCE="\"uplink\" in $CONFIG, now a port of $MASTER"
        fi
    else
        UPLINK=$DEFAULT_DEV
        UPLINK_SOURCE="the default route"
    fi
fi

if [[ -z $UPLINK ]]; then
    bad "there is no uplink to work on"
    echo "    ${UPLINK_SOURCE:-Nothing is configured and there is no default route.}"
    exit 1
fi

if [[ ! -e /sys/class/net/$UPLINK ]]; then
    bad "$UPLINK does not exist on this machine, and it is what was chosen"
    echo "    Chosen because $UPLINK_SOURCE."
    exit 1
fi

# Whether this uplink is also the way out decides what "it worked" means below.
CARRIES_DEFAULT=no
[[ $UPLINK == "$DEFAULT_DEV" ]] && CARRIES_DEFAULT=yes

if [[ $MODE == check ]]; then
    step "Uplink"

    ok "$UPLINK, because $UPLINK_SOURCE"

    if [[ $CARRIES_DEFAULT == no ]]; then
        echo "    The way out of this machine goes over ${DEFAULT_DEV:-nothing},"
        echo "    which is a different interface. That is a supported arrangement"
        echo "    and the usual reason for it is a wireless machine with a wired"
        echo "    card for the seats, since neither macvlan nor a bridge works on"
        echo "    802.11."
    fi

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

    # Only when the bridge is not the uplink itself, which it is once this has
    # run, and reporting the same seats twice reads as two separate findings.
    if [[ $BRIDGE != "$UPLINK" ]]; then
        list=$(seats_on bridged "$BRIDGE" | paste -sd' ')
        [[ -n $list ]] && ok "bridged on $BRIDGE: $list"
    fi

    step "The next boot"

    # The uplink is brought up at boot by whatever profile claims it, and when
    # two of them do, which one wins is a race. Losing it puts the address on
    # the bare interface instead of the bridge: no network for the host, no
    # seats on the segment, and somebody picking a connection by hand in the
    # network settings to get out of it. This is the line to read after a
    # reboot, and before trusting one.
    PHYS=$(nmcli -g connection.interface-name con show "$PORT_CON" 2>/dev/null || true)
    [[ -n $PHYS ]] || PHYS=$UPLINK

    mapfile -t CLAIMS < <(claimants "$PHYS")

    case ${#CLAIMS[@]} in
        0) warn "no profile brings $PHYS up by itself, so a reboot leaves it down" ;;
        1) ok "one profile brings $PHYS up at boot: ${CLAIMS[0]}" ;;
        *) bad "${#CLAIMS[@]} profiles want $PHYS at boot: ${CLAIMS[*]}"
           echo "    Which one wins is a race, and the boot that loses it comes up on"
           echo "    the wrong interface. Delete the ones that are not Polyseat's."
           ;;
    esac

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

# start_seats says which seats to start and does not start them.
#
# Deliberately. incus start brings the container up and nothing else: the seat's
# compositor and Sunshine are user units that are not enabled, and starting a
# seat properly also writes the Moonlight app list, brings up the audio stack,
# waits for Sunshine and reads back which encoder it got. That is the daemon's
# start path and it is reached from the interface. This script ran incus start
# once and left two seats that were up, on the network, and streaming nothing.
start_seats() {
    local name

    ((${#STOPPED[@]:-0})) || return 0

    step "Start these seats from the Polyseat interface"

    for name in "${STOPPED[@]:-}"; do
        [[ -n $name ]] || continue
        echo "    $name"
    done

    echo
    echo "    Not done here on purpose: starting a seat is more than starting"
    echo "    its container, and the daemon is the thing that knows the rest."

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

# release_uplink takes the interface away from whatever holds it now, in the one
# way that survives a reboot.
#
# connection.autoconnect=no is the obvious version, and for a saved profile it
# is the right one. For the default wired connection NetworkManager invents it
# is worse than doing nothing: that profile lives in /run, so the setting goes
# with it at shutdown and the profile is built again at the next boot with
# autoconnect back on. Two profiles then want the interface at boot, and the
# boot that hands it to the wrong one is a machine with its address on the bare
# interface. That is what this script did on its own, once per reboot, and it
# looked like the bridge itself being unreliable.
#
# Deleting it is what NetworkManager understands: a default wired connection
# that is deleted puts its device in /var/lib/NetworkManager/no-auto-default.state
# and is never invented again, so from then on exactly one profile can claim the
# interface.
release_uplink() {
    if [[ $CURRENT_GENERATED == yes ]]; then
        nmcli con delete uuid "$CURRENT_UUID" >/dev/null 2>&1 || true

        if nmcli -t -f UUID con show | grep -qxF "$CURRENT_UUID"; then
            warn "$CURRENT could not be deleted, switching it off instead"
        else
            ok "$CURRENT is gone, and NetworkManager will not invent it again"
            return 0
        fi
    fi

    nmcli con mod "$CURRENT" connection.autoconnect no
    nmcli con down "$CURRENT" >/dev/null 2>&1 || true
    ok "$CURRENT does not come up on its own any more"
}

# restore_uplink gives $1 back a profile that brings it up by itself, and says
# in $RESTORED which one that is.
#
# Where release_uplink deleted an invented profile there is nothing to switch
# back on, and nothing will be invented again either, so one is written. Saved
# this time, which is what the interface should have had all along: the reason
# any of this was ever a race is that it had no profile of its own.
restore_uplink() {
    local dev=$1

    if [[ -n ${CURRENT:-} && ${CURRENT_GENERATED:-yes} == no ]] &&
       nmcli -t -f NAME con show | grep -qxF "$CURRENT"; then
        RESTORED=$CURRENT
        nmcli con mod "$CURRENT" connection.autoconnect yes >/dev/null 2>&1 || true
        nmcli con up "$CURRENT" >/dev/null 2>&1
        return
    fi

    RESTORED="wired-$dev"

    nmcli -t -f NAME con show | grep -qxF "$RESTORED" ||
        nmcli con add type ethernet ifname "$dev" con-name "$RESTORED" \
            ipv4.method auto \
            ipv6.method auto \
            connection.autoconnect yes \
            connection.autoconnect-retries 0 >/dev/null

    nmcli con up "$RESTORED" >/dev/null 2>&1
}

# ------------------------------------------------------------------- undo

if [[ $MODE == undo ]]; then
    step "Removing the bridge"

    if ! nmcli -t -f NAME con show | grep -qx "$BRIDGE_CON"; then
        warn "no $BRIDGE_CON connection, so there is nothing of ours to remove"
        exit 0
    fi

    # Asked of the port connection rather than of the bridge: a bridge with
    # seats on it has their veths among its ports too, and the first name in
    # that directory is not necessarily the wire.
    PORT=$(nmcli -g connection.interface-name con show "$PORT_CON" 2>/dev/null || true)
    [[ -n $PORT ]] || PORT=$(ls /sys/class/net/"$BRIDGE"/brif 2>/dev/null | head -1)

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

    CURRENT=$(saved_wired_for "$PORT")

    # No saved wired profile left is the normal case rather than a surprise:
    # installing deleted the invented one and NetworkManager does not invent it
    # again, so restore_uplink writes a real one.
    CURRENT_GENERATED=no
    [[ -n $CURRENT ]] || CURRENT_GENERATED=yes

    nmcli con delete "$PORT_CON" >/dev/null 2>&1 || true
    nmcli con delete "$BRIDGE_CON" >/dev/null 2>&1 || true

    if restore_uplink "$PORT"; then
        ok "$RESTORED is up"
    else
        bad "$RESTORED did not come up, the machine may be off the network"
        echo "    By hand: nmcli con up \"$RESTORED\""
        exit 1
    fi

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

ok "$UPLINK, because $UPLINK_SOURCE"

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

CURRENT_UUID=$(nmcli -t -f UUID,DEVICE con show --active | awk -F: -v d="$UPLINK" '$2 == d { print $1; exit }')

if generated "$CURRENT_UUID"; then
    CURRENT_GENERATED=yes
    warn "which NetworkManager invented, so it is not a profile that survives a reboot"
else
    CURRENT_GENERATED=no
fi

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

    if restore_uplink "$UPLINK"; then
        ok "$RESTORED is up again"
    else
        bad "$RESTORED did not come up, the machine may be off the network"
        echo "    By hand: nmcli con up \"$RESTORED\""
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

# A bridge over an interface that was never the way out does not become the way
# out. Left to take a default route from its lease it would quietly move the
# machine's routing onto the card the seats hang off, which is a bigger change
# than the one being asked for and not one anybody would connect to this script.
ROUTING=()

if [[ $CARRIES_DEFAULT == no ]]; then
    ROUTING=(ipv4.never-default yes ipv6.never-default yes)
    ok "and it is not the way out, so $BRIDGE will not take the default route"
fi

nmcli con add type bridge ifname "$BRIDGE" con-name "$BRIDGE_CON" \
    "${ROUTING[@]}" \
    bridge.stp no \
    bridge.forward-delay 0 \
    bridge.mac-address "$MAC" \
    ipv4.method auto \
    ipv6.method auto \
    connection.autoconnect yes \
    connection.autoconnect-retries 0 >/dev/null || rollback "the bridge connection could not be created"

ok "$BRIDGE created with $UPLINK's address $MAC"

# The priority and the retries are what makes the next boot deterministic
# rather than lucky. Anything else that ever turns up for this interface carries
# the default priority of 0, or NetworkManager's -999 for one it invented, so
# this profile wins; and a first attempt that fails because the switch is not
# forwarding yet must not be the last attempt, which four and then nothing is.
nmcli con add type ethernet ifname "$UPLINK" con-name "$PORT_CON" \
    controller "$BRIDGE_CON" \
    port-type bridge \
    connection.autoconnect yes \
    connection.autoconnect-priority 100 \
    connection.autoconnect-retries 0 >/dev/null || rollback "the port connection could not be created"

ok "$UPLINK prepared as its port"

step "Moving the address over"

# The old connection is stopped from coming back before anything is taken down.
# Left able to come up it would race the port connection for the interface on
# the next boot, and which one won would decide whether the machine had a
# network. How it is stopped depends on what it is; see release_uplink.
release_uplink

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

# Only when this uplink is the way out. Asking a bridge over a card that never
# carried the default route to answer for the gateway is asking the wrong
# question, and rolling a correct run back on the answer would make the
# supported wireless arrangement impossible to set up.
if [[ $CARRIES_DEFAULT == yes ]]; then
    GATEWAY=$(ip -4 route show default dev "$BRIDGE" | awk '{ print $3; exit }')

    if [[ -n $GATEWAY ]] && ping -c1 -W2 "$GATEWAY" >/dev/null 2>&1; then
        ok "the gateway $GATEWAY answers over $BRIDGE"
    else
        rollback "nothing answers over $BRIDGE, so the machine is not really on the network"
    fi
else
    ok "the way out stays on ${DEFAULT_DEV:-nothing}, which this did not touch"
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
echo
echo "    The reboot is the part that used to go wrong, and --check reads it: it"
echo "    counts the profiles that want $UPLINK at boot, and one is the only good"
echo "    answer."
