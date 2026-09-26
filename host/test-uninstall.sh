#!/usr/bin/env bash
# Runs the two network questions in host/uninstall.sh against stubbed tools.
#
# That script deletes seats and packages and runs only as root, so it is not
# something CI or a developer can run. The two functions checked here decide
# whether an Incus network is deleted and whether somebody is told how to take
# the LAN bridge down, and the first of those can take a network away from a
# container that is not Polyseat's if it answers wrongly.
#
# incus and nmcli are replaced by scripts that answer what each case wants and
# record how they were called. It needs no root, no network and no Incus.
#
#   ./test-uninstall.sh
set -uo pipefail

HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

pass=0
fail=0

check_ok()  { printf '  \033[32m✓\033[0m %s\n' "$*"; pass=$((pass + 1)); }
check_bad() { printf '  \033[31m✗\033[0m %s\n' "$*"; fail=$((fail + 1)); }
step()      { printf '\n\033[1m%s\033[0m\n' "$*"; }

WORK=$(mktemp -d)
trap 'rm -rf -- "$WORK"' EXIT

# Lifted from the script by name rather than copied, for the reason
# test-lan-bridge.sh gives: a copy passes while the real one is broken.
FUNCTIONS=$WORK/functions.sh

sed -n '/^drop_management_bridge() {$/,/^}$/p; /^lan_bridge_port() {$/,/^}$/p' \
    "$HERE/uninstall.sh" > "$FUNCTIONS"

for name in drop_management_bridge lan_bridge_port; do
    grep -q "^$name() {$" "$FUNCTIONS" || {
        echo "could not lift $name out of uninstall.sh" >&2
        exit 1
    }
done

STUBS=$WORK/bin
mkdir -p "$STUBS"

# Each stub prints the file named after it and records its arguments.
for tool in incus nmcli; do
    cat > "$STUBS/$tool" <<'STUB'
#!/usr/bin/env bash
self=$(basename "$0")
printf '%s %s\n' "$self" "$*" >> "$STUB_LOG"
case "$self $*" in
    "incus network list"*) cat "$STUB_DIR/incus.list" 2>/dev/null ;;
    "incus network delete"*) exit "$(cat "$STUB_DIR/incus.delete" 2>/dev/null || echo 0)" ;;
    "nmcli -t -f NAME con show") cat "$STUB_DIR/nmcli.names" 2>/dev/null ;;
    "nmcli -g connection.interface-name"*) cat "$STUB_DIR/nmcli.port" 2>/dev/null ;;
esac
exit 0
STUB
    chmod 0755 "$STUBS/$tool"
done

# run executes a function in a shell of its own with the stubs first on PATH,
# and leaves its output in $WORK/out and the tool calls in $WORK/log.
run() {
    : > "$WORK/log"

    STUB_LOG=$WORK/log STUB_DIR=$WORK PATH="$STUBS:$PATH" bash -c '
        ok()   { echo "ok: $*"; }
        warn() { echo "warn: $*"; }
        . "$1"
        "$2"
    ' _ "$FUNCTIONS" "$1" > "$WORK/out" 2>&1
}

deleted() { grep -q '^incus network delete polyseatbr0$' "$WORK/log"; }

step "polyseatbr0"

printf 'incusbr0,3\npolyseatbr0,0\n' > "$WORK/incus.list"
run drop_management_bridge
if deleted; then check_ok "deleted when Incus counts nothing on it"; else check_bad "left alone with nothing on it: $(cat "$WORK/out")"; fi

printf 'incusbr0,0\npolyseatbr0,1\n' > "$WORK/incus.list"
run drop_management_bridge
if ! deleted && grep -q 'kept' "$WORK/out"; then
    check_ok "kept, and said so, while something is still on it"
else
    check_bad "deleted with a user left on it: $(cat "$WORK/out")"
fi

printf 'incusbr0,0\nmypolyseatbr0,0\npolyseatbr00,0\n' > "$WORK/incus.list"
run drop_management_bridge
if ! grep -q 'network delete' "$WORK/log"; then
    check_ok "a network whose name only contains it is not touched"
else
    check_bad "deleted something that was not polyseatbr0: $(grep delete "$WORK/log")"
fi

printf 'incusbr0,0\n' > "$WORK/incus.list"
run drop_management_bridge
if ! grep -q 'network delete' "$WORK/log" && [[ ! -s $WORK/out ]]; then
    check_ok "nothing said and nothing done when it does not exist"
else
    check_bad "acted on a bridge that is not there: $(cat "$WORK/out")"
fi

printf 'polyseatbr0,0\n' > "$WORK/incus.list"
echo 1 > "$WORK/incus.delete"
run drop_management_bridge
if grep -q '^warn: .*could not be removed' "$WORK/out"; then
    check_ok "a refused delete is reported rather than claimed"
else
    check_bad "a refused delete read as: $(cat "$WORK/out")"
fi
rm -f "$WORK/incus.delete"

step "The LAN bridge"

printf 'Wired connection 1\nlo\n' > "$WORK/nmcli.names"
run lan_bridge_port
if [[ ! -s $WORK/out ]]; then check_ok "nothing when lan-bridge.sh never ran"; else check_bad "named a port with no bridge: $(cat "$WORK/out")"; fi

printf 'polyseat-bridge\npolyseat-uplink\n' > "$WORK/nmcli.names"
echo enp5s0 > "$WORK/nmcli.port"
run lan_bridge_port
if [[ $(cat "$WORK/out") == enp5s0 ]]; then check_ok "names the port the bridge was built over"; else check_bad "named $(cat "$WORK/out")"; fi

printf 'not-polyseat-bridge\n' > "$WORK/nmcli.names"
run lan_bridge_port
if [[ ! -s $WORK/out ]]; then check_ok "a profile whose name only contains ours is not ours"; else check_bad "took $(cat "$WORK/out") for ours"; fi

printf '\n%d passed, %d failed\n' "$pass" "$fail"
((fail == 0))
