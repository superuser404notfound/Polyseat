#!/usr/bin/env bash
# What the host's package manager is called, and how to ask it things.
#
# Sourced, never run. Everything in host/ that used to say `pacman` says one of
# these instead, so that adding a third distribution is a row in the table below
# rather than an edit to five scripts.
#
# The seat is not in scope here and never will be. A seat is an Incus container
# built from archlinux/current, and it stays that on every host: the daemon runs
# pacman *inside* the container, which is a different machine from the one this
# file describes. internal/seat/provision.go and internal/seat/freshness.go are
# therefore untouched by any of this, and that is the reason host support is a
# small job rather than a rewrite.
#
# Sourced from three places and found in the same order everywhere: next to the
# script that wants it, then /usr/local/lib/polyseat, then /usr/lib/polyseat.
# That is the order a checkout install and a package already use for the input
# helpers, and it has to be script-first: a checkout's prepare.sh must read the
# checkout's copy of this file rather than the one an older package left in
# /usr/lib.
#
# Every function that answers a question returns a status rather than printing
# yes or no, and callers must write `if pkg_installed foo; then`. A bare
# `pkg_installed foo && ...` under `set -e` ends the calling script when the
# answer is no, which is the trap the rest of host/ already documents.

# --------------------------------------------------------------- what this is

# DISTRO_FAMILY is which of the three package managers this machine has, and it
# is the only thing the rest of host/ branches on. DISTRO_ID and DISTRO_NAME are
# for saying so out loud; nothing decides anything on them.
DISTRO_ID=""
DISTRO_NAME=""
DISTRO_FAMILY=""
DISTRO_VERSION_ID=""

# distro_detect reads /etc/os-release and works out which family this is.
#
# ID first, then ID_LIKE, because a derivative names its parent there and that
# is exactly the question being asked: CachyOS, EndeavourOS and Manjaro all say
# ID_LIKE=arch and all three have pacman. The README has said "Arch, or
# something based on it" since the first release, and this is where that "based
# on" is finally something other than a hope.
#
# Falls back to which binary exists when there is no os-release worth reading.
# A machine with pacman and no os-release is still a machine with pacman, and
# refusing it over a missing file would be pedantry.
#
# POLYSEAT_OS_RELEASE points this somewhere else, which is what host/test-distro.sh
# uses and the only reason it is not hard coded. Two of the three rows in this
# file describe machines this project is not developed on, and without a way to
# hold them against a file they would be read by nobody until somebody installed
# Polyseat on one.
distro_detect() {
    local id="" like="" name="" version=""
    local release=${POLYSEAT_OS_RELEASE:-/etc/os-release}

    if [[ -r $release ]]; then
        # Read in a subshell so that the file's own variables do not land in the
        # caller's environment. os-release sets NAME and VERSION among others,
        # and a script that sourced it directly would find its own $NAME quietly
        # replaced.
        eval "$(
            . "$release" 2>/dev/null
            printf 'id=%q like=%q name=%q version=%q\n' \
                "${ID:-}" "${ID_LIKE:-}" "${PRETTY_NAME:-${NAME:-}}" "${VERSION_ID:-}"
        )"
    fi

    DISTRO_ID=$id
    DISTRO_NAME=$name
    DISTRO_VERSION_ID=$version

    case " $id $like " in
        *" arch "*|*" archlinux "*|*" cachyos "*) DISTRO_FAMILY=arch ;;
        *" debian "*|*" ubuntu "*)                DISTRO_FAMILY=debian ;;
        *" fedora "*|*" rhel "*|*" centos "*)     DISTRO_FAMILY=fedora ;;
        *)
            # Nothing in os-release said. Ask the filesystem instead.
            if   command -v pacman  >/dev/null 2>&1; then DISTRO_FAMILY=arch
            elif command -v apt-get >/dev/null 2>&1; then DISTRO_FAMILY=debian
            elif command -v dnf     >/dev/null 2>&1; then DISTRO_FAMILY=fedora
            else DISTRO_FAMILY=""
            fi
            ;;
    esac

    [[ -n $DISTRO_NAME ]] || DISTRO_NAME=${DISTRO_ID:-this machine}

    [[ -n $DISTRO_FAMILY ]]
}

# distro_describe prints what was found, for a script that wants to say so.
distro_describe() {
    printf '%s' "${DISTRO_NAME:-unknown}"

    case "$DISTRO_FAMILY" in
        arch)   printf ' (pacman)' ;;
        debian) printf ' (apt)' ;;
        fedora) printf ' (dnf)' ;;
    esac

    printf '\n'
}

# distro_refuse prints the one message an unsupported machine should get, and
# returns non-zero so the caller can exit on it.
#
# It exists because the alternative was what these scripts did until now, which
# was to run until the first `pacman` and die with "pacman: command not found"
# somewhere in the middle of a step. That reads like Polyseat is broken rather
# than like this machine is not one it knows, and it says nothing about what to
# do next.
distro_refuse() {
    echo "  Polyseat's host scripts do not know this machine's package manager."
    echo
    echo "    found: ${DISTRO_NAME:-no /etc/os-release and no known package manager}"
    echo
    echo "  Supported hosts are Arch and anything based on it, Debian and"
    echo "  anything based on it, and Fedora. Everything a seat needs is"
    echo "  installed inside the seat, so this is a requirement of the host"
    echo "  scripts alone and not of the design."
    echo
    echo "  What a port needs is host/distro.sh, which is one table."

    return 1
}

# ------------------------------------------------------------- package naming

# pkg_name maps a name this project uses to the name this distribution uses.
#
# The left hand column is Polyseat's own vocabulary and appears nowhere else; a
# name with no row here is passed through unchanged, which is right for the
# many packages that are called the same thing everywhere.
#
# Only the host's packages are here. Nothing a seat installs belongs in this
# table, because a seat is Arch on every host.
pkg_name() {
    local want=$1

    case "$DISTRO_FAMILY:$want" in
        # Python is the input broker's language, and the daemon runs the broker
        # on the host. Arch's `python` is 3; the other two ship a `python`
        # that either is not there or is 2, and name 3 explicitly.
        debian:python|fedora:python) echo python3 ;;

        # Debian and Fedora both call the compiler golang. Arch calls it go.
        debian:go|fedora:go) echo golang ;;

        # vainfo, which the AMD path uses to ask the card whether it can encode.
        # Debian ships that one binary in a package named after it rather than
        # in the utilities bundle.
        debian:libva-utils) echo vainfo ;;

        *) echo "$want" ;;
    esac
}

# --------------------------------------------------------------- asking about

# pkg_installed says whether a package is installed on the host.
pkg_installed() {
    local p
    p=$(pkg_name "$1")

    case "$DISTRO_FAMILY" in
        arch)   pacman -Qq "$p" >/dev/null 2>&1 ;;
        # -W prints a line for a package dpkg has heard of, installed or not, so
        # the status field is what actually answers this. `ii` is installed and
        # configured; `rc` is removed with its config still there, which is not
        # installed and which a plain `dpkg -l` would have counted.
        debian) [[ "$(dpkg-query -W -f='${db:Status-Abbrev}' "$p" 2>/dev/null)" == ii* ]] ;;
        fedora) rpm -q "$p" >/dev/null 2>&1 ;;
        *)      return 1 ;;
    esac
}

# pkg_available says whether the repositories this machine is configured with
# can install a package. Asked before offering it, so that an offer is never
# made that cannot be taken.
pkg_available() {
    local p
    p=$(pkg_name "$1")

    case "$DISTRO_FAMILY" in
        arch)   pacman -Si "$p" >/dev/null 2>&1 ;;
        # policy rather than show, and the Candidate line rather than the exit
        # status: apt-cache show succeeds for a virtual package that nothing
        # provides, and apt-get install on one of those fails.
        debian) [[ -n "$(apt-cache policy "$p" 2>/dev/null | sed -n 's/^  Candidate: \(.*\)$/\1/p' | grep -v '^(none)$')" ]] ;;
        fedora) dnf -q list --available "$p" >/dev/null 2>&1 || dnf -q list --installed "$p" >/dev/null 2>&1 ;;
        *)      return 1 ;;
    esac
}

# pkg_first_available prints the first of several candidate names that this
# machine's repositories can actually supply, and nothing when none of them can.
#
# For the places where a package is named in a message rather than installed.
# The alternative was writing one name per distribution into this file and
# hoping, which ages badly: the NVIDIA packages carry a driver branch on two of
# the three, and Debian has renamed its 32 bit libraries more than once. Asking
# is cheap, reads the local metadata and cannot be wrong about this machine.
#
# Nothing found is a useful answer in its own right. On both Debian and Fedora
# the NVIDIA packages live in a repository that is off by default, so a search
# that comes back empty usually means non-free or RPM Fusion is not enabled,
# and the callers say so rather than printing a name that would not resolve.
pkg_first_available() {
    local candidate

    for candidate in "$@"; do
        if pkg_available "$candidate"; then
            echo "$candidate"

            return 0
        fi
    done

    return 1
}

# pkg_owner prints the package a file belongs to, or nothing.
#
# Used to tell a packaged installation from a checkout one, and to name the
# package somebody would reinstall to get a file back.
pkg_owner() {
    local f=$1

    case "$DISTRO_FAMILY" in
        arch)   pacman -Qoq -- "$f" 2>/dev/null | head -1 ;;
        # dpkg -S prints "package: /path", and package carries an architecture
        # qualifier on a multiarch system, so the first colon-separated field is
        # the name whether or not it is qualified.
        debian) dpkg -S -- "$f" 2>/dev/null | head -1 | cut -d: -f1 ;;
        fedora) rpm -qf --queryformat '%{NAME}\n' -- "$f" 2>/dev/null | head -1 ;;
    esac
}

# pkg_owns_file says whether any package owns a file.
pkg_owns_file() {
    [[ -n "$(pkg_owner "$1")" ]]
}

# ------------------------------------------------------------------- changing

# pkg_install installs packages, and refreshes first only where refreshing is
# safe.
#
# This is the one place where a straight translation of the pacman call would
# have been wrong, so it is worth writing down. On Arch this deliberately does
# not pass -Sy: refreshing the database and installing from it without upgrading
# is the partial upgrade Arch warns about, and an installer is a bad place to
# break somebody's system in a way that surfaces weeks later.
#
# Neither of the other two has that hazard. Debian and Fedora both support
# installing a single package against current metadata without dragging the rest
# of the system forward, and on Debian the opposite mistake is the real one: an
# apt-get install against lists that were fetched months ago fails on a 404 for
# a version that has since been superseded. So Debian refreshes and Arch does
# not, and that is the distributions differing rather than this file being
# inconsistent. dnf refreshes its own metadata when it is stale.
pkg_install() {
    local -a names=()
    local p

    for p in "$@"; do names+=("$(pkg_name "$p")"); done
    ((${#names[@]})) || return 0

    case "$DISTRO_FAMILY" in
        arch)
            pacman -S --needed --noconfirm "${names[@]}"
            ;;
        debian)
            # Recommends are off. On Debian incus recommends a good deal that a
            # headless host has no use for, and an installer that quietly pulls
            # in a stack of extras is not the neighbour this project wants to be.
            apt-get update -qq || true
            DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends "${names[@]}"
            ;;
        fedora)
            dnf install -y "${names[@]}"
            ;;
        *)
            return 1
            ;;
    esac
}

# pkg_install_file installs a package that is already on disk.
#
# What the update button runs, and the reason it is not simply "install this
# path": on Debian a bare filename is read as a package name, so the path has to
# be one apt recognises as a path. The caller passes an absolute one and this
# does not have to care, but a relative one would need ./ in front of it.
pkg_install_file() {
    local f=$1

    case "$DISTRO_FAMILY" in
        arch)   pacman -U --noconfirm -- "$f" ;;
        # install rather than dpkg -i, because install resolves the package's
        # dependencies and dpkg -i leaves them unmet and the package half
        # configured.
        debian) DEBIAN_FRONTEND=noninteractive apt-get install -y "$f" ;;
        fedora) dnf install -y "$f" ;;
        *)      return 1 ;;
    esac
}

# pkg_remove removes packages and leaves their dependencies alone.
#
# Leaving the dependencies is the whole point and is stated in three places in
# this project already: on a machine where the package manager pulled Incus in
# as a dependency of Polyseat, taking dependencies out with it would take
# somebody's container manager, and every container on the machine with it.
#
# Arch gets that by using -R rather than -Rs. Debian gets it by default, since
# apt-get remove touches nothing else. Fedora does not: dnf ships
# clean_requirements_on_remove=True, so a plain `dnf remove polyseat` behaves
# like `pacman -Rs` and would do exactly the damage the Arch call is written to
# avoid. Hence the setopt, which is not tidying.
pkg_remove() {
    local -a names=()
    local p

    for p in "$@"; do names+=("$(pkg_name "$p")"); done
    ((${#names[@]})) || return 0

    case "$DISTRO_FAMILY" in
        arch)   pacman -R --noconfirm "${names[@]}" ;;
        debian) DEBIAN_FRONTEND=noninteractive apt-get remove -y "${names[@]}" ;;
        fedora) dnf remove -y --setopt=clean_requirements_on_remove=False "${names[@]}" ;;
        *)      return 1 ;;
    esac
}

# ------------------------------------------------------------------ 32 bit

# multilib_enabled says whether this machine can install 32 bit libraries.
#
# Steam's own client is 32 bit and so are a great many games, so a seat whose
# host has no 32 bit driver userspace runs them without a GPU. Each of the three
# arranges it differently: Arch has a repository that is off by default, Debian
# has an architecture that has to be added to dpkg, and Fedora has multilib
# always on with no switch to find.
multilib_enabled() {
    case "$DISTRO_FAMILY" in
        arch)   grep -q '^\[multilib\]' /etc/pacman.conf 2>/dev/null ;;
        debian) dpkg --print-foreign-architectures 2>/dev/null | grep -qx i386 ;;
        fedora) return 0 ;;
        *)      return 1 ;;
    esac
}

# multilib_hint prints how to turn it on, for the warning that says it is off.
multilib_hint() {
    case "$DISTRO_FAMILY" in
        arch)   echo "Enable the multilib repository in /etc/pacman.conf first." ;;
        debian) echo "Run: sudo dpkg --add-architecture i386 && sudo apt-get update" ;;
    esac
}

# ------------------------------------------------------------------- upgrading

# upgrade_hint prints the command that brings this machine's package metadata
# and system up to date, named for the distribution rather than for Arch.
#
# Printed when an install fails, which on every one of the three is most often a
# machine whose idea of what exists is older than what the mirrors now carry.
upgrade_hint() {
    case "$DISTRO_FAMILY" in
        arch)   echo "sudo pacman -Syu" ;;
        debian) echo "sudo apt-get update && sudo apt-get upgrade" ;;
        fedora) echo "sudo dnf upgrade" ;;
    esac
}

# incus_hint says where Incus comes from when the configured repositories do not
# have it, which is the one prerequisite that is not everywhere.
#
# Debian has it in trixie and not in bookworm, where it wants backports or
# zabbly. Fedora has had it since 41. Neither is a thing this script will do
# behind somebody's back: adding a repository to a machine is a decision about
# who that machine trusts, and it belongs to the person who owns it.
incus_hint() {
    case "$DISTRO_FAMILY" in
        debian)
            echo "Incus is in Debian 13 (trixie) and newer. On 12 (bookworm) it"
            echo "comes from backports or from the zabbly repository:"
            echo "  https://github.com/zabbly/incus"
            ;;
        fedora)
            echo "Incus is in Fedora 41 and newer. On an older Fedora there is"
            echo "no package for it."
            ;;
        arch)
            echo "incus is in the extra repository. A database too old to see it"
            echo "is the usual reason, so try: $(upgrade_hint)"
            ;;
    esac
}

# install_hint prints the command somebody would type to install a package
# themselves, for the messages that suggest one rather than doing it.
install_hint() {
    case "$DISTRO_FAMILY" in
        arch)   echo "sudo pacman -S" ;;
        debian) echo "sudo apt-get install" ;;
        fedora) echo "sudo dnf install" ;;
    esac
}

# toolkit_hint says where nvidia-container-toolkit comes from.
#
# The one prerequisite that is not in Debian's repositories at all. It is what
# mirrors the host's driver userspace into every seat, so on an NVIDIA host it
# is not optional, and NVIDIA distributes it from a repository of their own.
# Fedora has since packaged it; the NVIDIA repository is still what NVIDIA's own
# instructions point at, and either works.
toolkit_hint() {
    case "$DISTRO_FAMILY" in
        arch)
            echo "nvidia-container-toolkit is in the extra repository."
            ;;
        debian)
            echo "nvidia-container-toolkit is not in Debian. It comes from"
            echo "NVIDIA's own repository, whose setup is three commands:"
            echo "  https://nvidia.github.io/libnvidia-container/"
            ;;
        fedora)
            echo "nvidia-container-toolkit is in Fedora as"
            echo "golang-github-nvidia-container-toolkit, and in NVIDIA's own"
            echo "repository under its plain name:"
            echo "  https://nvidia.github.io/libnvidia-container/"
            ;;
    esac
}

# nvidia32_hint prints how to get the 32 bit half of the driver userspace.
#
# Worked out from the 64 bit half rather than written down, and that turns out
# to be both shorter and more correct than a table would have been. A machine
# reaching this has a working 64 bit driver by definition — nvidia-smi answered
# two steps earlier — so the package owning libnvidia-encode.so.1 is installed
# on it right now, and the 32 bit package is that same name with this
# distribution's suffix on it. That is true on all three and it cannot name the
# wrong driver branch, because it is reading the branch already in use.
#
# The names it lands on are libnvidia-encode1:i386 on Debian,
# xorg-x11-drv-nvidia-libs.i686 on Fedora and lib32-nvidia-utils on Arch.
#
# The fallbacks are only reached when ldconfig or the package manager will not
# answer, which on a machine whose driver is working should not happen.
nvidia32_hint() {
    local lib pkg

    # The 64 bit entry specifically: (libc6) is the 32 bit one and
    # (libc6,x86-64) the 64 bit one, so the closing bracket is what separates
    # them. Taking the 32 bit path here would be asking what is already there.
    lib=$(ldconfig -p 2>/dev/null |
        awk '/libnvidia-encode\.so\.1 \(libc6,x86-64\)/ {print $NF; exit}')

    if [[ -n $lib ]]; then
        pkg=$(pkg_owner "$lib")
    fi

    case "$DISTRO_FAMILY" in
        arch)
            # Arch prefixes rather than suffixes, and the 32 bit packages are a
            # parallel set with lib32- in front.
            if [[ -n ${pkg:-} ]]; then
                echo "$(install_hint) lib32-$pkg"
            else
                echo "$(install_hint) lib32-nvidia-utils"
            fi
            ;;
        debian)
            # dpkg qualifies a package with an architecture after a colon, which
            # is what multiarch is.
            if [[ -n ${pkg:-} ]]; then
                echo "$(install_hint) $pkg:i386"
            else
                echo "$(install_hint) nvidia-driver-libs:i386"
            fi
            ;;
        fedora)
            # rpm puts the architecture after a dot, and multilib is always on,
            # so there is nothing to enable first.
            if [[ -n ${pkg:-} ]]; then
                echo "$(install_hint) $pkg.i686"
            else
                echo "$(install_hint) xorg-x11-drv-nvidia-libs.i686"
            fi
            ;;
    esac
}

# nvidia_driver_hint prints how this distribution installs the NVIDIA driver.
#
# Printed and never run, and that is the deliberate part. On Arch the module
# package name is derivable from the kernel package, so prepare.sh works it out
# and offers to install it. The other two build the module rather than ship it —
# Debian with DKMS, Fedora with akmods — and this only describes that.
#
# The package it names is searched for rather than written down. Nothing can be
# derived here the way nvidia32_hint derives its name, because the driver is by
# definition not installed at this point; what can be done is to ask this
# machine's repositories which of the plausible names they actually carry, so
# the command printed is one that will resolve.
#
# An empty search is the more useful answer of the two. On both distributions
# these packages live in a repository that is off by default, so nothing found
# means non-free or RPM Fusion has not been enabled, and saying that is worth
# more than a package name would have been.
nvidia_driver_hint() {
    local found

    case "$DISTRO_FAMILY" in
        debian)
            # nvidia-driver is the metapackage that pulls the module and the
            # userspace together. The others are what a machine on a different
            # branch or an older release has instead.
            found=$(pkg_first_available nvidia-driver nvidia-open-kernel-dkms \
                                        nvidia-tesla-driver nvidia-legacy-390xx-driver || true)

            echo "On Debian the driver and the module come from non-free."

            if [[ -n $found ]]; then
                echo
                echo "  $(install_hint) $found firmware-misc-nonfree"
                echo
                echo "DKMS builds the module during the install, so reboot afterwards."
            else
                echo
                echo "This machine's repositories carry no NVIDIA driver package at"
                echo "all, which means the non-free and non-free-firmware components"
                echo "are not enabled for this release. Enable them in"
                echo "/etc/apt/sources.list, run apt-get update, and try again."
            fi
            ;;
        fedora)
            # akmod builds the module against whichever kernel is running, which
            # is what a Fedora machine wants; kmod is the prebuilt one.
            found=$(pkg_first_available akmod-nvidia kmod-nvidia || true)

            echo "On Fedora the driver comes from RPM Fusion's nonfree."

            if [[ -n $found ]]; then
                echo
                echo "  $(install_hint) $found xorg-x11-drv-nvidia-cuda"
                echo
                echo "akmods builds the module in the background after the install"
                echo "rather than during it, so give it a few minutes before"
                echo "rebooting, and do not read the first failure as final."
            else
                echo
                echo "This machine's repositories carry no NVIDIA driver package at"
                echo "all, which means RPM Fusion's nonfree repository is not set up."
                echo "Its own instructions are at https://rpmfusion.org/Configuration,"
                echo "and this step is worth doing before coming back here."
            fi
            ;;
    esac
}
