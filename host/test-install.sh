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

# And the daemon's own credentials, because an unclaimed machine is the state
# under test: the first person to open the page chooses the password. Leaving
# them behind made the run after the first one check a machine that had already
# been claimed, and say so.
vm bash -c 'rm -f /var/lib/polyseat/credentials.json'

ok "group membership, idmap entries, the Incus setup and the credentials cleared"

check "Incus really is uninitialised now" \
    vm bash -c '! incus storage list --format csv 2>/dev/null | grep -q .'

step "Copying the repository in"
tar -C "$REPO" -cf - --exclude=.git --exclude=spike/artifacts . |
    vm bash -c "rm -rf /root/polyseat && mkdir -p /root/polyseat && tar -C /root/polyseat -xf -"
ok "copied"

# ------------------------------------------------------------- the thing itself

step "The driver check, which this machine cannot pass"
# The test machine is a virtual one with no GPU, so the installer refuses to run
# on it, and that refusal has to be tested rather than worked around silently:
# without it every check below would be testing an installer that never ran.
check "it refuses without a working driver" \
    vm bash -c '! SUDO_USER='"$TESTUSER"' bash /root/polyseat/host/install.sh </dev/null >/root/nogpu.log 2>&1'
check "and says why"                        \
    vm grep -qiE "no NVIDIA card|driver is not answering" /root/nogpu.log

step "Running install.sh"
# POLYSEAT_ALLOW_NO_GPU is the door the check above leaves open for exactly this:
# a machine that is knowingly without a GPU. Everything the installer does apart
# from that one check is the same.
# Kept, because two of its steps report rather than change anything and the only
# place their verdict exists is in what they printed.
if vm bash -c "SUDO_USER=$TESTUSER POLYSEAT_ALLOW_NO_GPU=1 bash /root/polyseat/host/install.sh >/root/install.log 2>&1; rc=\$?; cat /root/install.log; exit \$rc"; then
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

# A machine nobody has claimed offers to be claimed, and says so on the way up.
# If that were ever silent, the page would ask for a password that does not
# exist and the machine would be unusable with no sign of why.
# Scoped to this start of the unit rather than to the whole journal, which
# still holds every earlier run: the first version of this passed on a line
# written an hour before by a daemon that no longer existed.
check "it says it has no password yet" vm bash -c '
    id=$(systemctl show -p InvocationID --value polyseatd)
    journalctl _SYSTEMD_INVOCATION_ID="$id" --no-pager | grep -qi "no password yet"'
check "and the API agrees"               vm bash -c 'curl -sk --max-time 10 https://127.0.0.1:47800/api/session | grep -q "\"setup\":true"'

# Asked for over the network rather than by looking at a listening socket,
# because the certificate is generated on that first start too, and a listener
# that cannot complete a handshake is not an interface anybody can use.
check "the interface answers on 47800"   vm bash -c 'curl -sk --max-time 10 -o /dev/null -w "%{http_code}" https://127.0.0.1:47800/ | grep -qE "^(200|30[0-9])$"'

step "Running it a second time"
# An installer that only works on a machine it has never touched is an installer
# nobody can rerun after a change, which is exactly what happens on every update.
if vm env SUDO_USER="$TESTUSER" POLYSEAT_ALLOW_NO_GPU=1 bash /root/polyseat/host/install.sh >/dev/null 2>&1; then
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

# ------------------------------------------------------------------ the purge

step "Running install.sh --purge against a seat that exists"
# The part that cannot be tested by talking about it. --purge deletes containers,
# and the reason it exists is the order it does things in: the daemon supervises
# every seat and reads inside each running one every ten seconds, so deleting a
# container underneath it lands an exec in a shutdown and leaves Incus with a
# "Stopping instance" task that never finishes. That is what happened by hand on
# the development machine.
#
# A throwaway container standing in for a seat, from the smallest image going,
# because what is under test is the stopping and deleting and not what is inside.
PURGESEAT=seat-purgetest

vm bash -c "mkdir -p /var/lib/polyseat/seats && printf '{\"name\":\"$PURGESEAT\"}\n' > /var/lib/polyseat/seats/$PURGESEAT.json"

if vm bash -c "incus launch images:alpine/3.20 $PURGESEAT >/dev/null 2>&1"; then
    ok "a stand-in container is running as $PURGESEAT"

    check "it really is running" \
        vm bash -c "incus list $PURGESEAT -c ns -f csv | grep -q RUNNING"

    if vm bash -c "bash /root/polyseat/host/install.sh --purge --yes >/root/purge.log 2>&1"; then
        ok "the purge finished without an error"
    else
        bad "the purge failed"
        vm bash -c "tail -5 /root/purge.log" || true
    fi

    check "the container is gone"           vm bash -c "! incus list $PURGESEAT -c n -f csv | grep -q ."
    check "the daemon state is gone"        vm bash -c "! test -e /var/lib/polyseat"
    check "it did not take Incus with it"   vm bash -c "incus list -f csv >/dev/null"
else
    note "no container image could be fetched, the purge is untested in this run"
fi

step "Result"
printf '  %d passed, %d failed\n\n' "$pass" "$fail"

if ((fail)); then
    echo "  The machine is left running. Look at it with:"
    echo "    sudo incus exec $VM -- bash"
    exit 1
fi

echo "  The machine is left running so the next run is quick. Remove it with:"
echo "    $HERE/test-install.sh --destroy"
