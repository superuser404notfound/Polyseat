#!/usr/bin/env bash
# Checks how host/check-hardening.sh reads kernel.sysrq out of sysctl files.
#
# That reading decides whether --fix writes anything, so getting it wrong in
# either direction is a real fault: calling a machine pinned when a file that
# sorts later overrides the pin, or pinning when the only mention is a comment.
# Every case builds a sysctl tree of its own under a temporary directory, so
# nothing here reads or writes the machine's configuration. It needs no root.
#
#   ./test-hardening.sh
set -uo pipefail

HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

pass=0
fail=0

WORK=$(mktemp -d)
trap 'rm -rf -- "$WORK"' EXIT

POLYSEAT_SYSCTL_ROOT="$WORK/root"
export POLYSEAT_SYSCTL_ROOT

# shellcheck source=host/check-hardening.sh
. "$HERE/check-hardening.sh"

# After sourcing and not before, because the script has helpers of the same
# names and these are the ones that count. Defined first, a run of this file
# once reported "0 passed, 0 failed" with every line printed as a pass.
ok()   { printf '  \033[32m✓\033[0m %s\n' "$*"; pass=$((pass + 1)); }
bad()  { printf '  \033[31m✗\033[0m %s\n' "$*"; fail=$((fail + 1)); }
step() { printf '\n\033[1m%s\033[0m\n' "$*"; }

# put writes one sysctl file into the test tree.
put() {
    mkdir -p "$(dirname -- "$WORK/root$1")"
    printf '%s\n' "$2" > "$WORK/root$1"
}

reset() { rm -rf -- "$WORK/root"; mkdir -p "$WORK/root"; }

# expect compares what sysrq_configured says with what it should.
expect() {
    local what=$1 want=$2 got
    got=$(sysrq_configured)
    got=${got//"$WORK/root"/}

    if [[ $got == "$want" ]]; then
        ok "$what"
    else
        bad "$what: got '${got//$'\t'/ in }', wanted '${want//$'\t'/ in }'"
    fi
}

step "Which file sets kernel.sysrq"

reset
expect "nothing set anywhere reads as nothing" ""

reset
put /usr/lib/sysctl.d/50-default.conf $'# Use kernel.sysrq = 1 to allow all keys.\nkernel.sysrq = 16'
expect "a comment mentioning it is not a setting" $'16\t/usr/lib/sysctl.d/50-default.conf'

reset
put /etc/sysctl.d/10-mine.conf "# kernel.sysrq = 1"
expect "a file with only a comment sets nothing" ""

reset
put /usr/lib/sysctl.d/50-default.conf "kernel.sysrq = 16"
put /etc/sysctl.d/99-polyseat-sysrq.conf "kernel.sysrq = 0"
expect "a later file name in /etc wins over an earlier one in /usr/lib" $'0\t/etc/sysctl.d/99-polyseat-sysrq.conf'

reset
put /etc/sysctl.d/99-polyseat-sysrq.conf "kernel.sysrq = 16"
put /usr/lib/sysctl.d/99-zz-vendor.conf "kernel.sysrq = 1"
expect "a later file name in /usr/lib wins over ours in /etc" $'1\t/usr/lib/sysctl.d/99-zz-vendor.conf'

reset
put /usr/lib/sysctl.d/50-default.conf "kernel.sysrq = 1"
put /etc/sysctl.d/50-default.conf "kernel.sysrq = 16"
expect "the same name in /etc masks the one in /usr/lib" $'16\t/etc/sysctl.d/50-default.conf'

reset
put /run/sysctl.d/60-runtime.conf "-kernel/sysrq=176"
expect "a runtime file, the slash spelling and the leading dash all count" $'176\t/run/sysctl.d/60-runtime.conf'

reset
put /etc/sysctl.d/99-polyseat-sysrq.conf "kernel.sysrq = 16"
put /etc/sysctl.conf "kernel.sysrq = 1"
expect "/etc/sysctl.conf comes last" $'1\t/etc/sysctl.conf'

step "Which values are harmless"

for v in 0 2 16 18; do
    if sysrq_harmless "$v"; then ok "$v is harmless"; else bad "$v was called dangerous"; fi
done

for v in 1 4 8 32 64 128 176 256 438 ""; do
    if sysrq_harmless "$v"; then bad "'$v' was called harmless"; else ok "'$v' is not harmless"; fi
done

step "What --fix writes"

for pair in 0:0 16:16 18:18 1:16 176:16 438:16 garbage:16; do
    have=${pair%%:*} want=${pair#*:}
    got=$(sysrq_fix_value "$have")
    if [[ $got == "$want" ]]; then
        ok "running at $have, --fix pins $got"
    else
        bad "running at $have, --fix pins $got instead of $want"
    fi
done

printf '\n%d passed, %d failed\n' "$pass" "$fail"
((fail == 0))
