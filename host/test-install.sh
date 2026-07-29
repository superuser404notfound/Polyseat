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

# The package database brought up to date, and the system with it. Not part of
# what is being tested: a machine somebody is about to install on has a current
# database, and the installer deliberately does not refresh one itself, because
# refreshing without upgrading is the partial upgrade Arch warns about.
vm bash -c 'pacman -Syu --noconfirm --quiet >/dev/null 2>&1' ||
    { echo "could not bring the test machine up to date"; exit 1; }
ok "package database current"

# sudo, because the installer re-execs nothing but the harness passes SUDO_USER
# and a machine without sudo is not the case under test.
vm bash -c 'pacman -S --noconfirm --needed --quiet sudo >/dev/null 2>&1' || true

# One prerequisite deliberately taken away, so that the installer's package step
# has something to do on every run and not only on the first.
#
# -R rather than -Rns, and that is the whole point of this comment. bpftrace
# itself is a few megabytes but it depends on bcc, which depends on clang and
# gcc: taking its dependencies with it costs 555 MB of downloading on every
# single run to prove one thing that does not involve any of them.
vm bash -c 'pacman -R --noconfirm bpftrace >/dev/null 2>&1 || true'
ok "bpftrace removed, so the installer has a missing package to deal with"

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
# Kept, because two of its steps report rather than change anything and the only
# place their verdict exists is in what they printed.
if vm bash -c "SUDO_USER=$TESTUSER bash /root/polyseat/host/install.sh >/root/install.log 2>&1; rc=\$?; cat /root/install.log; exit \$rc"; then
    ok "it finished without an error"
else
    bad "it failed"
fi

step "What it should have done"

check "the missing package was installed" vm pacman -Qq bpftrace
check "and every other prerequisite"     vm pacman -Qq incus nvidia-container-toolkit python go
check "polyseatd is installed"           vm test -x /usr/local/bin/polyseatd
check "it runs and reports a version"    vm /usr/local/bin/polyseatd -version
check "the systemd unit is registered"   vm test -f /etc/systemd/system/polyseatd.service
check "systemd can read that unit"       vm systemctl cat polyseatd.service
check "the udev rule is in place"        vm test -f /etc/udev/rules.d/72-polyseat-hide.rules
check "the old rule number is not"       vm bash -c '! test -e /etc/udev/rules.d/70-polyseat-hide.rules'

# udevadm verify parses the file the way udevd will and reports what it does not
# understand. A rule with a mistake in it is still a file that exists, gets
# installed, and reloads without complaint, and the first sign of trouble would
# be a seat's gamepad reaching the host desktop.
check "udev parses the rule"             vm udevadm verify /etc/udev/rules.d/72-polyseat-hide.rules
check "udev reloads"                     vm udevadm control --reload

for f in broker.py device_owner.py uhid_observer.py fakeudev.py; do
    check "helper $f"                    vm test -x "/usr/local/lib/polyseat/$f"
done

check "root has an idmap range (subuid)" vm grep -qE '^root:1000000:1000000000$' /etc/subuid
check "root has an idmap range (subgid)" vm grep -qE '^root:1000000:1000000000$' /etc/subgid
check "incus.socket is enabled"          vm systemctl is-enabled incus.socket
check "incus is initialised"             vm bash -c 'incus storage list --format csv | grep -q .'
check "$TESTUSER is in the input group"  vm bash -c "id -nG $TESTUSER | tr ' ' '\n' | grep -qx input"

# The two steps that only report. A verdict either way is a pass; silence is
# not, because a check that never ran looks exactly like a filesystem that is
# fine.
check "it judged the library filesystem" \
    vm grep -qE 'shares blocks|cannot share blocks|whether it shares blocks' /root/install.log
check "it judged the network uplink" \
    vm grep -qE 'take a macvlan from it|is wireless|no default route' /root/install.log
check "the library probe left nothing behind" \
    vm bash -c '! find / -maxdepth 4 -name ".polyseat-probe.*" 2>/dev/null | grep -q .'

step "Does a working daemon come out of it"
# The point of the whole exercise, and the part that checking for installed
# files does not reach. An installer can put every file in the right place and
# still leave a machine where nothing runs: a unit that does not start, a
# daemon that exits on its first look at the system, an interface that never
# binds. So the last instruction the installer prints is carried out here, and
# what it promises is then asked for over the network.
if vm systemctl enable --now polyseatd >/dev/null 2>&1; then
    ok "systemctl enable --now polyseatd"
else
    bad "the unit would not start"
fi

sleep 5

check "the daemon is running"            vm systemctl is-active polyseatd
check "it did not fail and restart"      vm bash -c '[ "$(systemctl show -p NRestarts --value polyseatd)" = "0" ]'

# The first password is generated on the first start and only written to the
# log. Without it the interface exists and nobody can get in.
check "it printed a first password"      vm bash -c 'journalctl -u polyseatd --no-pager | grep -qi password'

# Asked for over the network rather than by looking at a listening socket,
# because the certificate is generated on that first start too, and a listener
# that cannot complete a handshake is not an interface anybody can use.
check "the interface answers on 47800"   vm bash -c 'curl -sk --max-time 10 -o /dev/null -w "%{http_code}" https://127.0.0.1:47800/ | grep -qE "^(200|30[0-9])$"'

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

check "the daemon was stopped"           vm bash -c '! systemctl is-active polyseatd >/dev/null 2>&1'
check "and disabled"                     vm bash -c '! systemctl is-enabled polyseatd >/dev/null 2>&1'
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
