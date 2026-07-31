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
#   sudo ./install.sh                    install
#   sudo ./install.sh --uninstall        remove Polyseat, keep the seats
#   sudo ./install.sh --purge            remove the seats as well
#   sudo ./install.sh --purge --library  and the shared game library
#
# --yes answers the question --purge asks.
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

STATEDIR=/var/lib/polyseat
LIBRARYDIR=/srv/polyseat/library

# state_of reports what Incus thinks of a container: RUNNING, STOPPED, ERROR or
# nothing at all.
#
# Asked of `incus list` rather than `incus info`, because a container whose stop
# has half finished answers `incus info` with "Invalid PID -1" and nothing else,
# which is exactly the case this has to survive.
state_of() {
    incus list "$1" -c ns -f csv 2>/dev/null | awk -F, -v n="$1" '$1 == n { print $2 }'
}

# stop_seat brings a container down, and insists.
#
# `incus stop` has been seen to return success and leave the container running,
# with a "Stopping instance" task of its own that never ends. Waiting longer does
# not help: what does is killing the processes in the container's cgroup and
# restarting the Incus daemon, which clears the stuck task. Both were needed to
# take this machine's seats down, so both are written here rather than left for
# the next person to work out under pressure.
stop_seat() {
    local name=$1 waited=0

    [[ -n "$(state_of "$name")" ]] || return 0

    # Under a timeout, because this call is the one that hangs. `incus stop` has
    # been seen to sit there for minutes with the server's own "Stopping
    # instance" task never finishing, and without a bound the loop below, which
    # exists precisely for that case, is never reached: the first version of this
    # waited three minutes inside the call it was supposed to be recovering from.
    timeout 45 incus stop "$name" >/dev/null 2>&1 || true

    while [[ "$(state_of "$name")" == "RUNNING" && $waited -lt 60 ]]; do
        sleep 2
        waited=$((waited + 2))
    done

    [[ "$(state_of "$name")" == "RUNNING" ]] || { ok "$name stopped"; return 0; }

    warn "$name did not stop when asked, killing what is left of it"

    local cg=/sys/fs/cgroup/lxc.payload.$name

    if [[ -e $cg/cgroup.kill ]]; then
        echo 1 > "$cg/cgroup.kill" 2>/dev/null || true
    elif [[ -e $cg/cgroup.procs ]]; then
        while read -r pid; do kill -9 "$pid" 2>/dev/null || true; done < "$cg/cgroup.procs"
    fi

    sleep 3
    systemctl restart incus.service 2>/dev/null || true
    sleep 5

    [[ "$(state_of "$name")" == "RUNNING" ]] && bad "$name is still running" || ok "$name stopped"
}

if [[ "${1:-}" == "--purge" ]]; then
    shift

    library=false
    assume_yes=false

    for arg in "$@"; do
        case "$arg" in
            --library) library=true ;;
            --yes) assume_yes=true ;;
            *) echo "unknown option: $arg"; exit 1 ;;
        esac
    done

    # The names come from the seat records and from nowhere else. Matching
    # container names against a pattern would put somebody's unrelated container
    # one typo away from being deleted.
    seats=()
    if [[ -d $STATEDIR/seats ]]; then
        for f in "$STATEDIR"/seats/*.json; do
            [[ -e $f ]] || continue
            seats+=("$(basename "$f" .json)")
        done
    fi

    step "This removes Polyseat and everything it built"
    echo "  seats to delete:      ${seats[*]:-none found}"
    echo "  daemon state:         $STATEDIR (seat definitions, pairings, the web password)"
    if $library; then
        echo "  shared game library:  $LIBRARYDIR, and its games with it"
    else
        echo "  shared game library:  $LIBRARYDIR is KEPT, so the games come back"
    fi
    echo "  packages and Incus:   left alone, they are not Polyseat's to remove"
    echo

    if ! $assume_yes; then
        read -r -p "  Type purge to go ahead: " answer
        [[ $answer == "purge" ]] || { echo "  nothing done"; exit 1; }
    fi

    # The daemon first, and this is the whole reason --purge exists rather than
    # being a paragraph in a document. It supervises every seat and reads inside
    # each running one every ten seconds; deleting a container underneath that
    # lands an exec in a shutdown, and Incus answers with a "Stopping instance"
    # task that never finishes. That is how this machine's seats had to be taken
    # apart by hand, and the order is the fix.
    step "Stopping the daemon before touching anything it owns"
    systemctl disable --now polyseatd.service 2>/dev/null || true
    ok "polyseatd stopped"

    if ((${#seats[@]})); then
        step "Deleting the seats"

        for name in "${seats[@]}"; do
            stop_seat "$name"

            if [[ -n "$(state_of "$name")" ]]; then
                timeout 180 incus delete -f "$name" >/dev/null 2>&1 && ok "$name deleted" ||
                    bad "$name could not be deleted"
            else
                ok "$name had no container"
            fi
        done
    fi

    step "Removing what the daemon kept"
    rm -rfv "$STATEDIR" /etc/polyseat

    if $library; then
        rm -rfv "$LIBRARYDIR"
    else
        ok "$LIBRARYDIR kept"
    fi

    # Everything the plain uninstall does, by running that path. The marker is
    # so that it does not sign off by promising the seats are untouched, having
    # just deleted them.
    POLYSEAT_PURGED=1 exec "$0" --uninstall
fi

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
    if [[ -n "${POLYSEAT_PURGED:-}" ]]; then
        ok "gone, seats and all. $LIBRARYDIR is the only thing that may be left."
    else
        ok "gone. Seats, their containers and $STATEDIR are untouched."
    fi

    # Said here because the order matters and is not obvious. The daemon has just
    # been stopped, so the containers can be removed safely now; doing it while it
    # was still running is what leaves Incus with a stop that never finishes.
    if [[ -d $STATEDIR/seats ]] && compgen -G "$STATEDIR/seats/*.json" >/dev/null; then
        echo
        echo "  The seats are still here. Now that the daemon is stopped they can be"
        echo "  removed safely, or leave them and they come back on the next install:"
        echo
        echo "    sudo $0 --purge"
    fi

    exit 0
fi

step "Graphics"
# Which card is in this machine decides everything downstream: which package
# this installer needs, which driver check has to pass, and what the daemon
# builds seats from. So it is worked out first. An AMD machine has no use for
# nvidia-container-toolkit, which is a shim for a driver that is not there.
#
# Two passes, and the second one is the reason this is not four lines. Render
# nodes are the better source, because a node exists only where a driver is
# bound and it names that driver, which is exactly what the daemon looks at
# (internal/seat/gpu.go). But a machine whose driver is missing has no render
# node at all, and that machine is precisely the one this script exists to
# help. So when the nodes say nothing, the PCI devices are asked instead: they
# are there whether a driver is or not.
#
# SYSFS is a variable for one reason: this machine has one card and cannot grow
# a second one, so the only way to find out whether this picks the right card
# out of two is to build the tree by hand and point it at that. See
# host/test-gpu-detect.sh.
SYSFS="${POLYSEAT_SYSFS:-/sys}"

gpu_vendor=""
gpu_node=""
gpu_driver=""

vendor_name() {
    case "$1" in
        0x10de) echo nvidia ;;
        0x1002) echo amd ;;
    esac
}

for node in "$SYSFS"/class/drm/renderD*; do
    [[ -r $node/device/vendor ]] || continue

    v=$(vendor_name "$(<"$node/device/vendor")")
    [[ -n $v ]] || continue

    # NVIDIA wins when both are in one machine, the same way the daemon
    # decides it. Anything else and the two would disagree about a machine
    # somebody built for exactly this test.
    if [[ -z $gpu_vendor ]] || [[ $v == nvidia && $gpu_vendor != nvidia ]]; then
        gpu_vendor=$v
        gpu_node=/dev/dri/$(basename "$node")
        gpu_driver=""
        [[ -e $node/device/driver ]] &&
            gpu_driver=$(basename "$(readlink -f "$node/device/driver")")
    fi
done

if [[ -z $gpu_vendor ]]; then
    for dev in "$SYSFS"/bus/pci/devices/*; do
        # Class 0x03 is a display controller. Without the class test an AMD
        # machine could match on something that is not a graphics card at all.
        [[ -r $dev/class && -r $dev/vendor ]] || continue
        [[ "$(<"$dev/class")" == 0x03* ]] || continue

        v=$(vendor_name "$(<"$dev/vendor")")
        [[ -n $v ]] || continue

        if [[ -z $gpu_vendor ]] || [[ $v == nvidia && $gpu_vendor != nvidia ]]; then
            gpu_vendor=$v
        fi
    done

    [[ -n $gpu_vendor ]] &&
        warn "an ${gpu_vendor^^} card is on the bus but no driver is bound to it"
fi

case "$gpu_vendor" in
    nvidia) ok "NVIDIA${gpu_driver:+, driver $gpu_driver}${gpu_node:+, $gpu_node}" ;;
    amd)    ok "AMD${gpu_driver:+, driver $gpu_driver}${gpu_node:+, $gpu_node}" ;;
    *)      warn "no NVIDIA or AMD card found" ;;
esac

if [[ $gpu_vendor == amd ]]; then
    echo "    The AMD path has never been run on real hardware. See docs/amd.md."
fi

step "Prerequisites"
missing=()
prereqs=(incus bpftrace python go)

# Only where it is the driver: libnvidia-container is what mirrors the host's
# driver into every seat, and on AMD nothing is mirrored because Mesa is a
# package the seat installs itself.
[[ $gpu_vendor == amd ]] || prereqs+=(nvidia-container-toolkit)

for pkg in "${prereqs[@]}"; do
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

if [[ $gpu_vendor == amd ]]; then
    step "AMD driver"
    # Far less to check than the other vendor, and that is the shape of AMD
    # rather than an omission here: there is no userspace on the host for a seat
    # to borrow. The kernel driver renders and encodes, Mesa lives inside the
    # seat as an ordinary package, and nothing is injected across the boundary.
    # That also means no host driver update can break a seat, which is the one
    # standing hazard on the NVIDIA side.
    if [[ -n "${POLYSEAT_ALLOW_NO_GPU:-}" ]]; then
        warn "POLYSEAT_ALLOW_NO_GPU is set, so the driver is not checked"
        echo "    Seats built on this machine will encode in software."
    elif [[ $gpu_driver != amdgpu ]]; then
        bad "the card is bound to ${gpu_driver:-no driver}, not amdgpu"
        echo "    amdgpu is what renders and encodes. A card that came up on"
        echo "    simpledrm or vesa can draw a picture and nothing else, so a"
        echo "    seat on it would stream in software."
        echo
        echo "    Missing firmware is the usual reason. Install"
        echo "    linux-firmware-amdgpu, reboot, and run this again."
        exit 1
    elif [[ ! -c $gpu_node ]]; then
        bad "$gpu_node is not a device, so nothing can render on this card"
        exit 1
    else
        ok "amdgpu is bound to the card and $gpu_node is there"

        # A warning rather than a refusal. Whether a seat encodes in hardware is
        # answered inside the seat, and the interface shows it; this is only the
        # cheapest place to find out before a seat has ever been built.
        if command -v vainfo >/dev/null 2>&1; then
            if vainfo --display drm --device "$gpu_node" 2>/dev/null |
                   grep -q VAEntrypointEncSlice; then
                ok "VA-API on this card can encode"
            else
                warn "VA-API on this card offers no encoder, seats would stream in software"
            fi
        else
            echo "    To confirm the card can encode before building a seat:"
            echo "      sudo pacman -S libva-utils"
            echo "      vainfo --display drm --device $gpu_node | grep EncSlice"
        fi
    fi
else
    step "NVIDIA driver"
    # The one hard requirement that is not a Polyseat package, and the one worth
    # refusing over: a seat built without a working driver comes up, streams in
    # software and looks entirely healthy. The encoder line on its card is the only
    # place it shows.
    #
    # What NVENC needs is the driver's own userspace, libcuda.so.1 and
    # libnvidia-encode.so.1, which nvidia-container-toolkit injects into every seat
    # from the host. Both belong to nvidia-utils, established with pacman -Qo rather
    # than assumed. The cuda package is the toolkit, nvcc and the CUDA runtime, and a
    # seat needs none of it.
    if [[ -n "${POLYSEAT_ALLOW_NO_GPU:-}" ]]; then
        warn "POLYSEAT_ALLOW_NO_GPU is set, so the driver is not checked"
        echo "    Seats built on this machine will encode in software."
    elif driver=$(nvidia-smi --query-gpu=driver_version --format=csv,noheader 2>/dev/null) &&
         [[ -n $driver ]]; then
        ok "driver $driver is loaded and answering"
    else
        # Whether there is a card at all was settled in the Graphics step, which
        # asks the PCI bus for exactly this case: a machine whose driver is
        # missing has no render node to be found by.
        if [[ $gpu_vendor != nvidia ]]; then
            bad "no NVIDIA or AMD card found in this machine"
            echo "    Polyseat encodes with NVENC on NVIDIA and VA-API on AMD."
            echo "    Nothing here works on another vendor's GPU, and a seat"
            echo "    would stream in software."
            exit 1
        fi

        # The module package name is derived from the kernel package and cannot be
        # guessed at: on this machine the module comes from linux-cachyos-nvidia-open,
        # on plain Arch it would be nvidia-open or nvidia-open-dkms, and installing
        # the wrong one leaves a machine with no graphics at all. So it is worked out
        # from what this kernel actually is and offered rather than assumed.
        kernel_pkg=$(pacman -Qoq "/usr/lib/modules/$(uname -r)/vmlinuz" 2>/dev/null | head -1 || true)

        # A module package that is already installed is not wanted again, and its
        # presence changes the answer: everything is there and the module is simply
        # not loaded, which a reboot fixes and an install does not.
        module=""
        have_module=false

        for candidate in "${kernel_pkg:+$kernel_pkg-nvidia-open}" \
                         "${kernel_pkg:+$kernel_pkg-nvidia}" \
                         nvidia-open-dkms nvidia-dkms; do
            [[ -n $candidate ]] || continue

            if pacman -Qq "$candidate" >/dev/null 2>&1; then
                have_module=true
                module=$candidate
                break
            fi

            if [[ -z $module ]] && pacman -Si "$candidate" >/dev/null 2>&1; then
                module=$candidate
            fi
        done

        wanted=()
        $have_module || [[ -z $module ]] || wanted+=("$module")
        pacman -Qq nvidia-utils >/dev/null 2>&1 || wanted+=(nvidia-utils)

        if grep -q "^\[multilib\]" /etc/pacman.conf 2>/dev/null &&
           ! pacman -Qq lib32-nvidia-utils >/dev/null 2>&1; then
            wanted+=(lib32-nvidia-utils)
        fi

        bad "the NVIDIA driver is not answering"
        echo

        if ((${#wanted[@]} == 0)); then
            echo "    An NVIDIA card is here and the driver packages are installed"
            echo "    ($module, nvidia-utils), so the module is simply not loaded."
            echo "    Reboot, or modprobe nvidia, and run this again."
            exit 1
        fi

        echo "    An NVIDIA card is here, but nvidia-smi does not answer, so either the"
        echo "    userspace or the kernel module is missing. For this kernel"
        echo "    (${kernel_pkg:-unknown}) that means:"
        echo
        echo "      ${wanted[*]}"
        echo

        if ((${#wanted[@]})) && [[ -t 0 ]] && [[ -z "${POLYSEAT_NO_DRIVER_INSTALL:-}" ]]; then
            read -r -p "    Install those now? [y/N] " answer

            if [[ $answer == y || $answer == Y ]]; then
                if pacman -S --needed --noconfirm "${wanted[@]}"; then
                    echo
                    ok "installed ${wanted[*]}"
                    echo "    The module is not loaded yet. Reboot, then run this again."
                else
                    echo
                    bad "that did not work, install the driver by hand and run this again"
                fi

                exit 1
            fi
        fi

        echo "    Install them, reboot, and run this again."
        exit 1
    fi

    # A warning and not a refusal: everything works without it except the 32 bit
    # games, and Steam's own client and a great many games are 32 bit.
    if pacman -Qq lib32-nvidia-utils >/dev/null 2>&1; then
        ok "lib32-nvidia-utils, so 32 bit games get the GPU too"
    elif [[ -z "${POLYSEAT_ALLOW_NO_GPU:-}" ]]; then
        warn "lib32-nvidia-utils is missing, so 32 bit games in a seat will not find the GPU"
        grep -q "^\[multilib\]" /etc/pacman.conf 2>/dev/null ||
            echo "    Enable the multilib repository in /etc/pacman.conf first."
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
#
# Restarted and not only enabled, and then waited for. "enable --now" does
# nothing to a unit systemd already counts as active, and a socket unit whose
# file changed underneath it while it was running is exactly that: active,
# holding no socket, and saying so only in its own status. That is what a
# reinstall of the incus package leaves behind, and the next command then fails
# with "dial unix /var/lib/incus/unix.socket: no such file or directory", which
# names a file rather than the reason.
systemctl enable incus.socket >/dev/null 2>&1 || true

if ! systemctl restart incus.socket >/dev/null 2>&1; then
    bad "incus.socket would not start"
    systemctl status incus.socket --no-pager -n 10 || true
    exit 1
fi

for _ in $(seq 1 30); do
    [[ -S /var/lib/incus/unix.socket ]] && break
    sleep 1
done

if [[ ! -S /var/lib/incus/unix.socket ]]; then
    bad "incus.socket is up but /var/lib/incus/unix.socket never appeared"
    echo "    Everything below talks to Incus over that socket, so there is no"
    echo "    point going on. What systemd makes of it:"
    systemctl status incus.socket --no-pager -n 10 || true
    exit 1
fi

ok "incus.socket is listening"

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
