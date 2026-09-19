#!/usr/bin/env bash
# Runs the seat handling in host/lan-bridge.sh, on a machine with no seats.
#
# That script stops seats, rebuilds the machine's network and starts nothing,
# so it is not something CI or a developer can run. Its seat handling is
# another matter: three short functions whose only outside world is incus, and
# the one bug reported against this script so far lived in exactly there.
#
# Issue #4 was a bad substitution, `${#STOPPED[@]:-0}`, on the last step of a
# run that had already done everything else. bash reports it when the line is
# reached and not before, so `bash -n` parses the file happily and shellcheck
# says nothing about it at any severity, both of which were measured rather
# than assumed. Nothing in this repository could have caught it, which is what
# this file changes.
#
# incus is replaced by a script that records how it was called, so nothing here
# touches a container. It needs no root, no network and no Incus.
#
#   ./test-lan-bridge.sh
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

# The functions under test, taken from the script itself rather than copied
# here, because a copy is a second version that passes while the real one is
# broken. They are lifted by name: a rename breaks this extraction loudly,
# which is the failure worth having.
FUNCTIONS=$WORK/functions.sh

sed -n '/^stop_seats() {$/,/^}$/p; /^start_seats() {$/,/^}$/p; /^trap start_seats EXIT$/p' \
    "$HERE/lan-bridge.sh" > "$FUNCTIONS"

for name in stop_seats start_seats; do
    grep -q "^$name() {$" "$FUNCTIONS" || {
        echo "could not lift $name out of lan-bridge.sh" >&2
        exit 1
    }
done

# The safety net is a line of top level code rather than a function, and it is
# the half of issue #4 that was not the substitution: a run that dies after
# stopping seats has to say which ones. Lifted with them so that this checks
# the script's trap and not a copy of it.
grep -q '^trap start_seats EXIT$' "$FUNCTIONS" || {
    echo "lan-bridge.sh no longer names the stopped seats from its exit path" >&2
    exit 1
}

# run executes one case in a shell of its own, with the script's own options,
# the stubs below and whatever setup the case needs. A subshell per case
# because stop_seats exits on a seat that will not stop, and because a case
# must not be able to leave STOPPED behind for the next one.
run() {
    local setup=$1 body=$2

    bash -c '
set -euo pipefail

ok()   { printf "ok %s\n" "$*"; }
bad()  { printf "bad %s\n" "$*"; }
step() { printf "step %s\n" "$*"; }

# The two answers the functions ask the world for. RUNNING lists the seats that
# are up; REFUSES names one that will not stop, which is the path that reports
# and exits.
running() { [[ " ${RUNNING:-} " == *" $1 "* ]]; }

# The seat is the last argument: the real call is
# `incus stop --timeout 90 <name>`.
incus() {
    local name=${*: -1}

    printf "incus %s\n" "$*" >&2

    if [[ $name == "${REFUSES:-}" ]]; then
        return 1
    fi

    RUNNING=${RUNNING//$name/}
    return 0
}

STOPPED=()

'"$setup"'
source "$FUNCTIONS"
'"$body"'
' 2>/dev/null
}

expect() {
    local what=$1 want=$2 got=$3

    if [[ $got == *"$want"* ]]; then
        ok "$what"
    else
        bad "$what"
        note "wanted a line with: $want"
        note "got: ${got//$'\n'/ | }"
    fi
}

refute() {
    local what=$1 unwanted=$2 got=$3

    if [[ $got != *"$unwanted"* ]]; then
        ok "$what"
    else
        bad "$what"
        note "did not want: $unwanted"
        note "got: ${got//$'\n'/ | }"
    fi
}

export FUNCTIONS

step "Nothing was stopped"
# The case the reported crash happened in as well: the guard is the broken line,
# so an empty list died there too rather than returning quietly.
out=$(run 'RUNNING=""' 'stop_seats seat1 seat2; start_seats; echo "returned $?"')
expect "start_seats returns without a word"       "returned 0" "$out"
refute "and says nothing about starting anything" "step Start" "$out"

step "Two seats were stopped"
out=$(run 'RUNNING="seat1 seat2"' 'stop_seats seat1 seat2; start_seats; echo "returned $?"')
expect "the heading is printed"   "step Start these seats" "$out"
expect "seat1 is named"           "    seat1"              "$out"
expect "seat2 is named"           "    seat2"              "$out"
expect "and the call returns"     "returned 0"             "$out"

step "The list is shown once"
out=$(run 'RUNNING="seat1"' 'stop_seats seat1; start_seats; start_seats; echo "returned $?"')
expect "the seat is named"                    "    seat1"   "$out"
expect "the second call returns"              "returned 0"  "$out"
if [[ $(grep -c "step Start these seats" <<<"$out") -eq 1 ]]; then
    ok "and the heading is printed only once"
else
    bad "the heading was printed more than once"
fi

step "A seat that will not stop"
out=$(run 'RUNNING="seat1 seat2"; REFUSES=seat2' 'stop_seats seat1 seat2; echo "not reached"')
expect "it says so"                              "bad seat2 would not stop" "$out"
expect "the seat it had already stopped is named" "    seat1"               "$out"
refute "and it does not go on"                    "not reached"             "$out"

step "A run that dies after stopping a seat"
# Nothing calls start_seats here. The trap is what has to name the seat, because
# this is the shape the bug was reported in: everything done, the shell back
# with a failure, and two seats down with nothing said about them.
out=$(run 'RUNNING="seat1 seat2"' 'stop_seats seat1 seat2; exit 1')
expect "the heading is printed anyway" "step Start these seats" "$out"
expect "seat1 is named"                "    seat1"              "$out"
expect "seat2 is named"                "    seat2"              "$out"

step "Summary"
printf '  %d passed, %d failed\n\n' "$pass" "$fail"

((fail == 0))
