#!/usr/bin/env bash
# Checks host/distro.sh against all three package managers, on a machine that
# has one of them.
#
# The other two rows of that table describe machines this project is not
# developed on and CI does not run on, so without this they would be read by
# nobody until somebody installed Polyseat on a Debian box and found out. That
# is the same gap docs/amd.md describes for the AMD path, and this is the part
# of it that can be closed without hardware: what a stub cannot prove is that
# apt-get behaves as expected, and what it does prove is that distro.sh calls
# apt-get and not pacman, with the arguments that were meant.
#
# Every package manager is replaced by a script that records how it was called
# and answers whatever the test wants, so nothing here installs, removes or
# reads anything on the machine running it. It needs no root and no network.
#
#   ./test-distro.sh
set -uo pipefail

HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

pass=0
fail=0

ok()   { printf '  \033[32m✓\033[0m %s\n' "$*"; pass=$((pass + 1)); }
bad()  { printf '  \033[31m✗\033[0m %s\n' "$*"; fail=$((fail + 1)); }
note() { printf '    %s\n' "$*"; }
step() { printf '\n\033[1m%s\033[0m\n' "$*"; }

WORK=$(mktemp -d)
trap 'rm -rf -- "$WORK"' EXIT

STUB_DIR="$WORK/bin"
STUB_LOG="$WORK/log"
export STUB_LOG

mkdir -p "$STUB_DIR"

# One script under every name distro.sh might reach for. It writes down how it
# was called and says nothing back unless the test asked it to.
for name in pacman apt-get apt-cache dpkg dpkg-query rpm dnf ldconfig; do
    cat > "$STUB_DIR/$name" <<'STUB'
#!/usr/bin/env bash
self=$(basename "$0")
printf '%s %s\n' "$self" "$*" >> "$STUB_LOG"
[[ -r "${0%/*}/.out.$self" ]] && cat "${0%/*}/.out.$self"
# A per-command status, because the hints below need one tool to fail while the
# others go on answering: a repository that does not carry a package is exactly
# one command saying no.
[[ -r "${0%/*}/.exit.$self" ]] && exit "$(cat "${0%/*}/.exit.$self")"
exit "${STUB_EXIT:-0}"
STUB
    chmod 0755 "$STUB_DIR/$name"
done

PATH="$STUB_DIR:$PATH"
export PATH

# shellcheck source=host/distro.sh
. "$HERE/distro.sh"

# become makes distro.sh believe it is on a given distribution.
become() {
    local id=$1 like=${2:-}

    {
        printf 'ID=%s\n' "$id"
        [[ -n $like ]] && printf 'ID_LIKE="%s"\n' "$like"
        printf 'PRETTY_NAME="a test"\n'
    } > "$WORK/os-release"

    POLYSEAT_OS_RELEASE="$WORK/os-release" distro_detect
}

# said runs something and returns what the stubs recorded while it ran.
said() {
    : > "$STUB_LOG"
    "$@" >/dev/null 2>&1
    cat "$STUB_LOG"
}

# forget throws away whatever the stubs were told to say, so one section's
# setup cannot leak into the next one's assertions.
forget() { rm -f "$STUB_DIR"/.out.* "$STUB_DIR"/.exit.*; }

# expect checks that a call produced a line containing a fragment.
expect() {
    local what=$1 want=$2 got=$3

    if [[ $got == *"$want"* ]]; then
        ok "$what"
    else
        bad "$what"
        note "wanted a call containing: $want"
        note "got: ${got:-nothing at all}"
    fi
}

# reject checks that a call did NOT contain a fragment, which is how the
# dangerous defaults are held down.
reject() {
    local what=$1 unwanted=$2 got=$3

    if [[ $got != *"$unwanted"* ]]; then
        ok "$what"
    else
        bad "$what"
        note "must not contain: $unwanted"
        note "got: $got"
    fi
}

# ---------------------------------------------------------------------- names

step "Which family a machine turns out to be"
for case in "arch::arch" "cachyos:arch:arch" "endeavouros:arch:arch" \
            "debian::debian" "ubuntu:debian:debian" "linuxmint:ubuntu debian:debian" \
            "fedora::fedora" "rocky:rhel centos fedora:fedora"; do
    IFS=: read -r id like want <<< "$case"

    become "$id" "$like"

    if [[ $DISTRO_FAMILY == "$want" ]]; then
        ok "$id is $want"
    else
        bad "$id came out '$DISTRO_FAMILY', wanted $want"
    fi
done

step "Package names that differ"
become arch
[[ $(pkg_name python) == python ]] && ok "arch: python" || bad "arch: python is $(pkg_name python)"
[[ $(pkg_name go) == go ]]         && ok "arch: go"     || bad "arch: go is $(pkg_name go)"

become debian
[[ $(pkg_name python) == python3 ]]     && ok "debian: python3"     || bad "debian: python is $(pkg_name python)"
[[ $(pkg_name go) == golang ]]          && ok "debian: golang"      || bad "debian: go is $(pkg_name go)"
[[ $(pkg_name libva-utils) == vainfo ]] && ok "debian: vainfo"      || bad "debian: libva-utils is $(pkg_name libva-utils)"
[[ $(pkg_name incus) == incus ]]        && ok "debian: incus passes through" || bad "debian: incus is $(pkg_name incus)"

become fedora
[[ $(pkg_name python) == python3 ]] && ok "fedora: python3" || bad "fedora: python is $(pkg_name python)"
[[ $(pkg_name go) == golang ]]      && ok "fedora: golang"  || bad "fedora: go is $(pkg_name go)"

# -------------------------------------------------------------------- install

step "Installing reaches the right tool"
become arch
out=$(said pkg_install incus python)
expect "arch installs with pacman -S --needed" "pacman -S --needed --noconfirm incus python" "$out"
# The partial upgrade Arch warns about, and the reason the Arch branch is the
# one that does not refresh.
reject "arch does not refresh first" "-Sy " "$out"

become debian
out=$(said pkg_install incus python)
expect "debian refreshes first"        "apt-get update"  "$out"
expect "debian installs the mapped name" "install -y --no-install-recommends incus python3" "$out"
reject "debian does not call pacman"   "pacman"          "$out"

become fedora
out=$(said pkg_install incus python)
expect "fedora installs with dnf" "dnf install -y incus python3" "$out"

step "Installing a file on disk"
become arch
expect "arch: pacman -U" "pacman -U --noconfirm -- /tmp/p.pkg.tar.zst" "$(said pkg_install_file /tmp/p.pkg.tar.zst)"
become debian
# install rather than dpkg -i, because install resolves dependencies and dpkg -i
# leaves them unmet and the package half configured.
out=$(said pkg_install_file /tmp/p.deb)
expect "debian: apt-get install on the path" "apt-get install -y /tmp/p.deb" "$out"
reject "debian: not dpkg -i"                 "dpkg -i"                       "$out"
become fedora
expect "fedora: dnf install on the path" "dnf install -y /tmp/p.rpm" "$(said pkg_install_file /tmp/p.rpm)"

# --------------------------------------------------------------------- remove

step "Removing leaves dependencies alone"
# The whole point, and stated in three places in this project: on a machine
# where the package manager pulled Incus in as a dependency of Polyseat, taking
# dependencies out with it takes somebody's container manager and every
# container on the machine with it.
become arch
out=$(said pkg_remove polyseat)
expect "arch: pacman -R" "pacman -R --noconfirm polyseat" "$out"
reject "arch: not -Rs"   "-Rs"                            "$out"
reject "arch: not -Rns"  "-Rns"                           "$out"

become debian
out=$(said pkg_remove polyseat)
expect "debian: apt-get remove" "apt-get remove -y polyseat" "$out"
reject "debian: not purge"      "purge"                      "$out"
reject "debian: not autoremove" "autoremove"                 "$out"

become fedora
out=$(said pkg_remove polyseat)
# dnf ships clean_requirements_on_remove=True, so a plain `dnf remove polyseat`
# behaves like `pacman -Rs` and does exactly the damage the Arch call is written
# to avoid. This is the one place where the safe behaviour has to be asked for.
expect "fedora: dnf remove"                  "dnf remove -y"                            "$out"
expect "fedora: keeps dependencies, and says so" "clean_requirements_on_remove=False" "$out"

# --------------------------------------------------------------------- asking

step "Asking whether a package is installed"
become arch
expect "arch: pacman -Qq" "pacman -Qq incus" "$(said pkg_installed incus)"
become fedora
expect "fedora: rpm -q"   "rpm -q incus"     "$(said pkg_installed incus)"

become debian
# dpkg-query -W prints a line for a package dpkg has merely heard of, so the
# status is what answers this. "rc" is removed-with-config, which is not
# installed and which a plain `dpkg -l` would have counted.
printf 'ii ' > "$STUB_DIR/.out.dpkg-query"
if pkg_installed incus; then ok "debian: ii counts as installed"; else bad "debian: ii was not counted as installed"; fi

printf 'rc ' > "$STUB_DIR/.out.dpkg-query"
if pkg_installed incus; then bad "debian: rc was counted as installed"; else ok "debian: rc does not count as installed"; fi
rm -f "$STUB_DIR/.out.dpkg-query"

step "Asking who owns a file"
become arch
expect "arch: pacman -Qoq" "pacman -Qoq -- /usr/bin/polyseatd" "$(said pkg_owner /usr/bin/polyseatd)"
become debian
expect "debian: dpkg -S"   "dpkg -S -- /usr/bin/polyseatd"     "$(said pkg_owner /usr/bin/polyseatd)"
become fedora
expect "fedora: rpm -qf"   "rpm -qf"                           "$(said pkg_owner /usr/bin/polyseatd)"

step "32 bit support is asked the way each one arranges it"
become arch
expect "arch: reads pacman.conf" "" "$(said multilib_enabled)"
if grep -q multilib <<< "$(declare -f multilib_enabled)"; then
    ok "arch: looks for the multilib repository"
else
    bad "arch: no longer looks for multilib"
fi

become debian
expect "debian: asks dpkg for foreign architectures" "dpkg --print-foreign-architectures" "$(said multilib_enabled)"

become fedora
# Always on, with no switch to find, so this must answer yes without calling
# anything at all.
out=$(said multilib_enabled)
if multilib_enabled && [[ -z $out ]]; then
    ok "fedora: always on, and asks nobody"
else
    bad "fedora: answered no, or went looking for a switch"
    note "got: ${out:-nothing}"
fi

step "Every hint says something"
for id in arch debian fedora; do
    become "$id"

    for fn in upgrade_hint install_hint incus_hint toolkit_hint nvidia32_hint; do
        if [[ -n "$($fn)" ]]; then
            ok "$id: $fn"
        else
            bad "$id: $fn said nothing"
        fi
    done
done

# nvidia_driver_hint is deliberately empty on Arch, because Arch is the one that
# offers to install the driver rather than describing how.
become arch
[[ -z "$(nvidia_driver_hint)" ]] && ok "arch: nvidia_driver_hint stays quiet, prepare.sh offers instead" \
                                 || bad "arch: nvidia_driver_hint spoke, and prepare.sh never reaches it"
for id in debian fedora; do
    become "$id"
    [[ -n "$(nvidia_driver_hint)" ]] && ok "$id: nvidia_driver_hint" || bad "$id: nvidia_driver_hint said nothing"
done

step "The 32 bit hint is derived from the driver that is already there"
# Not written down, because the package name carries a driver branch on two of
# the three. The machine reaching this has a working 64 bit driver, so the
# package owning libnvidia-encode.so.1 is the branch in use and the 32 bit name
# is that one with a suffix.
forget
printf '\tlibnvidia-encode.so.1 (libc6,x86-64) => /usr/lib/libnvidia-encode.so.1\n' \
    > "$STUB_DIR/.out.ldconfig"

become arch
printf 'nvidia-utils\n' > "$STUB_DIR/.out.pacman"
expect "arch: lib32- in front of what owns the library" "lib32-nvidia-utils" "$(nvidia32_hint)"

become debian
printf 'libnvidia-encode1: /usr/lib/x86_64-linux-gnu/libnvidia-encode.so.1\n' > "$STUB_DIR/.out.dpkg"
expect "debian: :i386 after it" "libnvidia-encode1:i386" "$(nvidia32_hint)"

become fedora
printf 'xorg-x11-drv-nvidia-libs\n' > "$STUB_DIR/.out.rpm"
expect "fedora: .i686 after it" "xorg-x11-drv-nvidia-libs.i686" "$(nvidia32_hint)"

# And when nothing answers, a name rather than an empty sentence.
forget
become debian
expect "debian: falls back to a name when nothing answers" "i386" "$(nvidia32_hint)"
become fedora
expect "fedora: falls back to a name when nothing answers" "i686" "$(nvidia32_hint)"

step "The driver hint names a package the repositories actually carry"
forget

# Debian's pkg_available reads apt-cache policy for a Candidate line, so this is
# what a repository that has the package looks like.
become debian
printf '  Candidate: 535.183.01-1\n' > "$STUB_DIR/.out.apt-cache"
out=$(nvidia_driver_hint)
expect "debian: names the package it found" "nvidia-driver" "$out"
expect "debian: and says DKMS builds it"    "DKMS"          "$out"

# Fedora's answers on exit status, so refusing is a status.
become fedora
echo 1 > "$STUB_DIR/.exit.dnf"
out=$(nvidia_driver_hint)
expect "fedora: says RPM Fusion is not set up" "RPM Fusion" "$out"
reject "fedora: does not print a package that would not resolve" "install akmod-nvidia" "$out"

forget
become fedora
out=$(nvidia_driver_hint)
expect "fedora: names akmod-nvidia when it is there" "akmod-nvidia" "$out"

# Debian with nothing available is the default, since apt-cache says nothing.
forget
become debian
out=$(nvidia_driver_hint)
expect "debian: says non-free is not enabled" "non-free" "$out"
reject "debian: prints no unresolvable package" "install nvidia-driver " "$out"

forget

step "An unknown machine"
# No os-release worth reading and no package manager on PATH: the fallback finds
# nothing and everything refuses rather than guessing.
: > "$WORK/os-release"
saved_path=$PATH
# Deliberately clobbered, which is the whole point of this section: with no
# package manager reachable, the fallback in distro_detect has nothing to find.
# shellcheck disable=SC2123
PATH=$WORK/nothing

if POLYSEAT_OS_RELEASE="$WORK/os-release" distro_detect; then
    bad "an unknown machine was recognised as $DISTRO_FAMILY"
else
    ok "distro_detect refuses it"
fi

PATH=$saved_path

if pkg_install incus 2>/dev/null; then bad "pkg_install went ahead anyway"; else ok "pkg_install refuses"; fi
if pkg_remove polyseat 2>/dev/null; then bad "pkg_remove went ahead anyway"; else ok "pkg_remove refuses"; fi
if pkg_installed incus 2>/dev/null; then bad "pkg_installed claimed to know"; else ok "pkg_installed says no"; fi
[[ -n "$(distro_refuse)" ]] && ok "distro_refuse explains itself" || bad "distro_refuse said nothing"

printf '\n\033[1m%d passed, %d failed\033[0m\n' "$pass" "$fail"
[[ $fail -eq 0 ]]
