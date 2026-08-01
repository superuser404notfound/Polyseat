#!/usr/bin/env bash
# Builds the Arch package and installs it on a throwaway machine.
#
# The sibling of test-install.sh, for the other way in. That one covers the
# checkout install, which places files under /usr/local and prepares the machine
# in one command. This covers what somebody installing from the AUR gets: a
# package that may place files and may not prepare anything, plus
# polyseat-prepare run by hand afterwards.
#
# Worth its own script because the two paths differ in the places that are easy
# to get wrong and impossible to notice here. The unit points somewhere else,
# the helpers live somewhere else, and the daemon has to find them without being
# told. None of that can be exercised on the machine this was written on, which
# has the checkout install and would answer every question with it.
#
#   ./test-package.sh              build the VM if needed, install, check
#   ./test-package.sh --rebuild    throw the VM away first
#   ./test-package.sh --destroy    remove the VM and stop
#
# makepkg refuses to run as root, so the package is built as the invoking user
# and only the Incus half runs privileged.
set -euo pipefail

VM=polyseat-pkg-test
IMAGE=images:archlinux/current
HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd -- "$HERE/.." && pwd)"
TESTUSER=tester

pass=0
fail=0

ok()   { printf '  \033[32m✓\033[0m %s\n' "$*"; pass=$((pass + 1)); }
bad()  { printf '  \033[31m✗\033[0m %s\n' "$*"; fail=$((fail + 1)); }
note() { printf '    %s\n' "$*"; }
step() { printf '\n\033[1m%s\033[0m\n' "$*"; }

# The user to build as, worked out before anything re-execs itself as root.
BUILDER=${SUDO_USER:-$USER}

vm() { sudo incus exec "$VM" -- "$@"; }

check() {
    local what=$1
    shift

    if "$@" >/dev/null 2>&1; then
        ok "$what"
    else
        bad "$what"
    fi
}

case "${1:-}" in
    --destroy)
        sudo incus delete -f "$VM" 2>/dev/null && echo "$VM is gone" || echo "$VM did not exist"
        exit 0
        ;;
    --rebuild)
        sudo incus delete -f "$VM" 2>/dev/null || true
        ;;
esac

[[ $BUILDER != root ]] || {
    echo "run this from your own account, not as root: makepkg refuses to build as root"
    exit 1
}

# ------------------------------------------------------------- the package

step "Building the package"

WORK=$(mktemp -d /tmp/polyseat-pkg.XXXXXX)
chmod 0755 "$WORK"
trap 'rm -rf "$WORK"' EXIT

cp "$REPO/packaging/aur/PKGBUILD" "$REPO/packaging/aur/polyseat.install" "$WORK/"

# Built from this checkout rather than from the release the PKGBUILD names, or
# this would test the last release every time and never the change in front of
# it. Everything else about the PKGBUILD is used as written: the same build
# flags, the same file layout, the same install file.
version=$(git -C "$REPO" -c safe.directory="$REPO" describe --tags --always | sed 's/^v//')
tarver=${version%%-*}

git -C "$REPO" -c safe.directory="$REPO" archive --format=tar.gz \
    --prefix="Polyseat-$tarver/" HEAD -o "$WORK/polyseat-$tarver.tar.gz"

sed -i -e "s/^pkgver=.*/pkgver=$tarver/" \
       -e 's|^source=.*|source=("$pkgname-$pkgver.tar.gz")|' \
       -e "s/^sha256sums=.*/sha256sums=('SKIP')/" "$WORK/PKGBUILD"

note "building polyseat $tarver from this checkout as $BUILDER"

# The whole build runs as the builder, redirect included. Two reasons: makepkg
# writes src/, pkg/ and the package itself into this directory, so it has to own
# it, and a redirect written outside the sudo does not run under it either.
#
# The comment above this one used to begin with the linter's own name, which it
# reads as a directive rather than as prose and then refuses the whole file for.
sudo chown -R "$BUILDER" "$WORK"

if ! sudo -u "$BUILDER" bash -c "cd ${WORK@Q} && makepkg -f --noconfirm >${WORK@Q}/build.log 2>&1"; then
    bad "makepkg failed"
    tail -20 "$WORK/build.log"
    exit 1
fi

PKG=$(ls "$WORK"/polyseat-*.pkg.tar.* | head -1)
ok "built $(basename "$PKG")"

# What the package contains is worth checking here rather than only in the VM,
# because a missing file shows up there as a daemon that will not start and here
# as the name of the file.
#
# Listed once into a variable rather than piped into grep per entry, and that
# is not a tidiness thing. `tar tf | grep -q` under `set -o pipefail` reports
# failure whenever grep finds its match early enough to close the pipe while tar
# is still writing: tar takes SIGPIPE and the pipeline fails. The first run of
# this said the package carried neither binary, because those two are the
# earliest entries in the archive, and the later ones passed by luck of timing.
contents=$(tar tf "$PKG" 2>/dev/null)

for want in usr/bin/polyseatd usr/bin/polyseat-prepare \
            usr/lib/polyseat/broker.py usr/lib/polyseat/uhid_observer.py \
            usr/lib/systemd/system/polyseatd.service \
            usr/lib/udev/rules.d/72-polyseat-hide.rules; do
    if grep -qx "$want" <<<"$contents"; then
        ok "carries $want"
    else
        bad "does not carry $want"
    fi
done

# The unit ships pointing at /usr/local/bin, which is right for the checkout
# install and wrong for a package. If the rewrite is ever lost, the unit starts
# nothing and says only "No such file or directory".
unit=$(tar -xOf "$PKG" usr/lib/systemd/system/polyseatd.service 2>/dev/null)

if grep -qx 'ExecStart=/usr/bin/polyseatd' <<<"$unit"; then
    ok "the unit points at /usr/bin/polyseatd"
else
    bad "the unit does not point at /usr/bin/polyseatd"
fi

# --------------------------------------------------------------- the machine

step "The test machine"

if ! sudo incus info "$VM" >/dev/null 2>&1; then
    note "creating $VM from $IMAGE, this downloads a virtual machine image"

    sudo incus create "$IMAGE" "$VM" --vm \
        -c security.secureboot=false \
        -c limits.memory=4GiB \
        -c limits.cpu=4 \
        -d root,size=16GiB >/dev/null
fi

if [[ "$(sudo incus info "$VM" | awk '/^Status:/ {print $2}')" != "RUNNING" ]]; then
    sudo incus start "$VM" >/dev/null
fi

for _ in $(seq 1 60); do
    state=$(vm systemctl is-system-running 2>/dev/null || true)
    [[ $state == running || $state == degraded ]] && break
    sleep 5
done

[[ ${state:-} == running || ${state:-} == degraded ]] || {
    echo "the test machine did not come up"
    exit 1
}

ok "$VM is up ($state)"

vm bash -c 'pacman -Syu --noconfirm --quiet >/dev/null 2>&1' ||
    { echo "could not bring the test machine up to date"; exit 1; }
ok "package database current"

vm bash -c "id $TESTUSER >/dev/null 2>&1 || useradd -m $TESTUSER"

# Back to the state a machine is in before any of this, every run. Without this
# the second run tests a machine the first one already prepared, which is the
# one kind of test this file exists to avoid.
vm bash -c 'pacman -R --noconfirm polyseat >/dev/null 2>&1 || true'
vm bash -c 'systemctl stop polyseatd >/dev/null 2>&1 || true'
vm bash -c 'rm -rf /var/lib/polyseat /etc/polyseat'
vm bash -c "gpasswd -d $TESTUSER input >/dev/null 2>&1 || true"
vm bash -c "sed -i '/^root:/d' /etc/subuid /etc/subgid 2>/dev/null || true"
vm bash -c 'incus profile device remove default root >/dev/null 2>&1 || true'
vm bash -c 'incus profile device remove default eth0 >/dev/null 2>&1 || true'
vm bash -c 'incus storage delete default >/dev/null 2>&1 || true'
vm bash -c 'incus network delete incusbr0 >/dev/null 2>&1 || true'
vm bash -c 'systemctl disable incus.socket >/dev/null 2>&1 || true'
ok "the machine is back to never having had Polyseat"

# ------------------------------------------------------------ installing it

step "pacman -U"

sudo incus file push "$PKG" "$VM/root/" >/dev/null 2>&1
check "the package installs" \
    vm bash -c "pacman -U --noconfirm /root/$(basename "$PKG")"

check "polyseatd is in /usr/bin"          vm test -x /usr/bin/polyseatd
check "polyseat-prepare is in /usr/bin"   vm test -x /usr/bin/polyseat-prepare
check "the helpers are in /usr/lib"       vm test -f /usr/lib/polyseat/broker.py
check "the unit is registered"            vm systemctl cat polyseatd.service

# pacman's own hooks do this, which is why the package does not.
check "udev has the rule"                 vm test -f /usr/lib/udev/rules.d/72-polyseat-hide.rules

# The version has to survive packaging, or the interface shows "dev" and the
# update check refuses to run because it cannot compare a development build
# with a release.
if vm polyseatd -version | grep -q "v$tarver"; then
    ok "it reports v$tarver rather than dev"
else
    bad "it reports $(vm polyseatd -version)"
fi

# A package must not have prepared anything. This is the whole reason the two
# halves are split, so it is checked rather than assumed.
check "it did NOT write the idmap range"  vm bash -c '! grep -q "^root:" /etc/subuid'
# Asked of the disk and not of the CLI. Reaching for `incus storage list` here
# starts incusd through socket activation, so the question changes the thing it
# is asking about, and the answer depends on how far that start has got. The
# pool is a directory either way.
check "it did NOT initialise Incus"       vm bash -c '! test -d /var/lib/incus/storage-pools/default'
check "it did NOT touch the group"        vm bash -c "! id -nG $TESTUSER | tr ' ' '\n' | grep -qx input"

# -------------------------------------------------------------- preparing it

step "polyseat-prepare"

# No GPU in a virtual machine, which is the one thing a test machine cannot
# have. The variable is the same escape hatch the installer offers.
if sudo incus exec "$VM" --env POLYSEAT_ALLOW_NO_GPU=1 --env SUDO_USER=$TESTUSER \
    -- polyseat-prepare >/dev/null 2>&1; then
    ok "it ran from the package, with no checkout anywhere on the machine"
else
    bad "polyseat-prepare failed"
    sudo incus exec "$VM" --env POLYSEAT_ALLOW_NO_GPU=1 --env SUDO_USER=$TESTUSER \
        -- polyseat-prepare 2>&1 | tail -15
fi

check "root has an idmap range"           vm grep -q '^root:' /etc/subuid
check "and a gid range"                   vm grep -q '^root:' /etc/subgid
check "incus.socket is up"                vm test -S /var/lib/incus/unix.socket
check "incus is initialised"              vm bash -c 'incus storage list --format csv | grep -q .'
check "$TESTUSER is in the input group"   vm bash -c "id -nG $TESTUSER | tr ' ' '\n' | grep -qx input"

# ---------------------------------------------------------- does it run

step "Does a working daemon come out of it"

vm systemctl enable --now polyseatd >/dev/null 2>&1 || true
sleep 6

check "the unit is active"                vm systemctl is-active --quiet polyseatd
check "it did not fail and restart"       vm bash -c '[ "$(systemctl show -p NRestarts --value polyseatd)" = "0" ]'
check "it is the unit from the package"   vm bash -c '[ "$(systemctl show -p FragmentPath --value polyseatd)" = "/usr/lib/systemd/system/polyseatd.service" ]'

# The point of the whole exercise. The daemon is told nothing about where its
# helpers are and has to find them, and on this machine the only copy is the
# one under /usr, which is the path a checkout install never uses.
if vm bash -c 'polyseatd -report 2>/dev/null | grep -q "\"helper_dir\": \"/usr/lib/polyseat\""'; then
    ok "it found its helpers under /usr/lib without being told"
else
    bad "it did not resolve the helper directory to /usr/lib/polyseat"
    vm bash -c 'polyseatd -report 2>/dev/null | grep helper_dir' || true
fi

check "the interface answers on 47800" \
    vm bash -c 'curl -sk -o /dev/null https://127.0.0.1:47800/'

check "and it wants a password set"  \
    vm bash -c 'curl -sk https://127.0.0.1:47800/api/session | grep -q "\"setup\":true"'

# ------------------------------------------------------------- removing it

step "pacman -R"

check "the package removes"               vm bash -c 'pacman -R --noconfirm polyseat'
check "the binary is gone"                vm bash -c '! test -e /usr/bin/polyseatd'
check "the helpers are gone"              vm bash -c '! test -e /usr/lib/polyseat'
check "the unit is gone"                  vm bash -c '! test -e /usr/lib/systemd/system/polyseatd.service'
check "the udev rule is gone"             vm bash -c '! test -e /usr/lib/udev/rules.d/72-polyseat-hide.rules'

# Left behind on purpose, and it is worth a test because the opposite would be
# somebody's seats and pairings deleted by an uninstall.
check "the daemon state is kept"          vm test -d /var/lib/polyseat
check "the idmap range is kept"           vm grep -q '^root:' /etc/subuid

step "Result"
printf '  %d passed, %d failed\n\n' "$pass" "$fail"

if ((fail)); then
    exit 1
fi

echo "  The machine is left running so the next run is quick. Remove it with:"
echo "    $HERE/test-package.sh --destroy"
