#!/usr/bin/env bash
# Runs host/install.sh against a throwaway machine and checks what it did.
#
# The installer is the one piece that cannot be exercised on the machine it was
# written on. Everything it does is already done here, so running it proves only
# that it is idempotent, and the steps that matter most are exactly the ones a
# developed-on machine never reaches: the idmap entry CachyOS does not ship, the
# first `incus admin init`, the group an account is not yet in.
#
# So it runs in a virtual machine rather than a container. A container shares the
# host's kernel and its udev, cannot run systemd units that touch devices, and
# would need nesting to run Incus inside it. A VM has its own kernel, its own
# udev and its own systemd, which is what the installer talks to.
#
#   ./test-install.sh              build the VM if needed, install, check
#   ./test-install.sh --rebuild    throw the VM away first
#   ./test-install.sh --keep       leave the VM running afterwards (the default
#                                  is to leave it, this only reads better)
#   ./test-install.sh --destroy    remove the VM and stop
#
# Needs to reach Incus, so it runs itself under sudo if it is not already root.
set -euo pipefail

VM=polyseat-test
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

[[ $EUID -eq 0 ]] || exec sudo -- "$0" "$@"

vm() { incus exec "$VM" -- "$@"; }

# check <description> <command...>: the command's exit status is the verdict.
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
        incus delete -f "$VM" 2>/dev/null && echo "$VM is gone" || echo "$VM did not exist"
        exit 0
        ;;
    --rebuild)
        incus delete -f "$VM" 2>/dev/null || true
        ;;
esac

# ---------------------------------------------------------------- the machine

step "The test machine"

if ! incus info "$VM" >/dev/null 2>&1; then
    note "creating $VM from $IMAGE, this downloads a virtual machine image"

    # security.secureboot=false because the Arch image is not signed for it and
    # the instance refuses to start otherwise, with an error that arrives at
    # first boot rather than at creation.
    incus create "$IMAGE" "$VM" --vm \
        -c security.secureboot=false \
        -c limits.memory=4GiB \
        -c limits.cpu=4 \
        -d root,size=16GiB >/dev/null
fi

if [[ "$(incus info "$VM" | awk '/^Status:/ {print $2}')" != "RUNNING" ]]; then
    incus start "$VM" >/dev/null
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

step "Bringing it to the state a fresh host is in"

# Not part of what is being tested: a machine somebody is about to install on
# has these, and the installer's own prerequisite check is what reports them
# when it does not.
vm bash -c 'pacman -Sy --noconfirm --needed --quiet incus nvidia-container-toolkit bpftrace python go sudo >/dev/null 2>&1' ||
    { echo "could not install the prerequisites in the test machine"; exit 1; }
ok "prerequisites installed"

# An unprivileged account, because the group step reads SUDO_USER and there has
# to be somebody for it to act on.
vm bash -c "id $TESTUSER >/dev/null 2>&1 || useradd -m $TESTUSER"
ok "$TESTUSER exists"

# The three states the installer is supposed to find and change. Cleared every
# run so that a second run tests the same thing as the first: without this the
# only run that ever tested anything would be the first one.
vm bash -c "gpasswd -d $TESTUSER input >/dev/null 2>&1 || true"
vm bash -c "sed -i '/^root:/d' /etc/subuid /etc/subgid 2>/dev/null || true"

# Incus back to never initialised, which is the part that is easy to forget and
# was: the first run leaves a storage pool behind, so from the second run on the
# check that Incus is initialised passed whether or not the installer had done
# anything at all. A test that only tests on a machine nobody has run it on is
# the one kind this whole file exists to avoid.
vm bash -c 'incus profile device remove default root >/dev/null 2>&1 || true'
vm bash -c 'incus profile device remove default eth0 >/dev/null 2>&1 || true'
vm bash -c 'incus storage delete default >/dev/null 2>&1 || true'
vm bash -c 'incus network delete incusbr0 >/dev/null 2>&1 || true'
vm bash -c 'systemctl disable incus.socket >/dev/null 2>&1 || true'

ok "group membership, idmap entries and the Incus setup cleared"

check "Incus really is uninitialised now" \
    vm bash -c '! incus storage list --format csv 2>/dev/null | grep -q .'

step "Copying the repository in"
tar -C "$REPO" -cf - --exclude=.git --exclude=spike/artifacts . |
    vm bash -c "rm -rf /root/polyseat && mkdir -p /root/polyseat && tar -C /root/polyseat -xf -"
ok "copied"

# ------------------------------------------------------------- the thing itself

step "Running install.sh"
if vm env SUDO_USER="$TESTUSER" bash /root/polyseat/host/install.sh; then
    ok "it finished without an error"
else
    bad "it failed"
fi

step "What it should have done"

check "polyseatd is installed"           vm test -x /usr/local/bin/polyseatd
check "it runs and reports a version"    vm /usr/local/bin/polyseatd -version
check "the systemd unit is registered"   vm test -f /etc/systemd/system/polyseatd.service
check "systemd can read that unit"       vm systemctl cat polyseatd.service
check "the udev rule is in place"        vm test -f /etc/udev/rules.d/72-polyseat-hide.rules
check "the old rule number is not"       vm bash -c '! test -e /etc/udev/rules.d/70-polyseat-hide.rules'
check "udev accepts the rule"            vm bash -c 'udevadm control --reload && ! udevadm test /sys/class/input/event0 2>&1 | grep -qi "72-polyseat.*invalid"'

for f in broker.py device_owner.py uhid_observer.py fakeudev.py; do
    check "helper $f"                    vm test -x "/usr/local/lib/polyseat/$f"
done

check "root has an idmap range (subuid)" vm grep -qE '^root:1000000:1000000000$' /etc/subuid
check "root has an idmap range (subgid)" vm grep -qE '^root:1000000:1000000000$' /etc/subgid
check "incus.socket is enabled"          vm systemctl is-enabled incus.socket
check "incus is initialised"             vm bash -c 'incus storage list --format csv | grep -q .'
check "$TESTUSER is in the input group"  vm bash -c "id -nG $TESTUSER | tr ' ' '\n' | grep -qx input"

step "Running it a second time"
# An installer that only works on a machine it has never touched is an installer
# nobody can rerun after a change, which is exactly what happens on every update.
if vm env SUDO_USER="$TESTUSER" bash /root/polyseat/host/install.sh >/dev/null 2>&1; then
    ok "it is idempotent"
else
    bad "it fails when run twice"
fi

check "still one idmap entry, not two"   vm bash -c "[ \$(grep -cE '^root:' /etc/subuid) -eq 1 ]"

step "Running install.sh --uninstall"
if vm bash /root/polyseat/host/install.sh --uninstall >/dev/null 2>&1; then
    ok "it finished without an error"
else
    bad "it failed"
fi

check "polyseatd is gone"                vm bash -c '! test -e /usr/local/bin/polyseatd'
check "the unit is gone"                 vm bash -c '! test -e /etc/systemd/system/polyseatd.service'
check "the udev rule is gone"            vm bash -c '! test -e /etc/udev/rules.d/72-polyseat-hide.rules'
check "the helpers are gone"             vm bash -c '! test -e /usr/local/lib/polyseat'

# Deliberately kept, and the test says so rather than leaving it unexamined.
check "the idmap entry is kept"          vm grep -qE '^root:' /etc/subuid
check "the group membership is kept"     vm bash -c "id -nG $TESTUSER | tr ' ' '\n' | grep -qx input"

step "Result"
printf '  %d passed, %d failed\n\n' "$pass" "$fail"

if ((fail)); then
    echo "  The machine is left running. Look at it with:"
    echo "    sudo incus exec $VM -- bash"
    exit 1
fi

echo "  The machine is left running so the next run is quick. Remove it with:"
echo "    $HERE/test-install.sh --destroy"
