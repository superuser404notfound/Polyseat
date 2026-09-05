#!/usr/bin/env bash
# What the whole chain actually did.
#
# Run it once with nobody streaming, to see whether Sunshine read the output's
# colour at all, and again with a Moonlight client connected and HDR switched on
# in its settings, which is the only way to see the answer that matters.
set -uo pipefail
source "$(dirname "$0")/lib.sh"

# The current Sunshine invocation, not a guessed number of lines. Same reason as
# everywhere else here: the interesting lines are written when the capture is
# first set up, which is the earliest thing Sunshine does.
log() { unitlog polyseat-sunshine.service; }

step "Which Sunshine is running"
exe=$(insh "pgrep -u $PLAYER -x sunshine | head -1")
if [ -n "$exe" ]; then
    path=$(insh "readlink -f /proc/$exe/exe")
    echo "    $path"
    case "$path" in
        "$PREFIX"/*) ok "the patched build" ;;
        *)           bad "the packaged build - the drop-in from 40-sunshine.sh is not in effect"; exit 1 ;;
    esac
else
    bad "Sunshine is not running"
    exit 1
fi

step "Did it read the output's colour"
# The line the patch adds. Three outcomes and they mean different things:
#   no color-management-v1     the compositor is too old, or LD_LIBRARY_PATH lost
#   did not describe           the compositor has the protocol but refused
#   primaries 6, tf 11 (HDR)   BT.2020 and PQ, which is the answer we want
line=$(log | grep -E '\[wlgrab\] (Output colour|The compositor)' | tail -1)
if [ -z "$line" ]; then
    # Not a verdict on the binary: the step above already established which one
    # is running by reading /proc/<pid>/exe. The line is written when a wlr
    # capture is set up, which Sunshine does while probing encoders at startup
    # and again for each stream, so its absence means the capture path was never
    # reached rather than that the patch is missing.
    warn "the patched build is running but has not described an output yet"
    warn "  it writes that line when it sets up a wlr capture; if Sunshine failed"
    warn "  to reach the compositor at all, that is above this in its log:"
    log | grep -iE 'wlgrab|wayland|Platform failed|encoder' | tail -8 | sed 's/^/    /'
else
    echo "    ${line#*] }"
    case "$line" in
        *"(HDR)"*) ok "Sunshine sees an HDR output" ;;
        *"(SDR)"*) bad "Sunshine sees an SDR output - go back to 30-hdr.sh" ;;
        *)         bad "Sunshine could not read the colour" ;;
    esac
fi

step "The encoder Sunshine chose"
log | grep -E 'Found H\.26[45] encoder|Found AV1 encoder' | tail -3 | sed 's/^/    /'

step "What went out on the wire"
# Only present once somebody has streamed. This is the decisive line: Sunshine
# prints the colour coding it settled on when it builds the encode session, and
# "HDR (Rec. 2020 + SMPTE 2084 PQ)" is the thing this whole spike exists to see.
coding=$(log | grep 'Color coding:' | tail -1)
depth=$(log | grep 'Color depth:' | tail -1)
if [ -z "$coding" ]; then
    warn "nobody has streamed yet"
    warn "  connect Moonlight, switch HDR on in its settings for this host, start"
    warn "  the Desktop application, then run this script again"
else
    echo "    ${coding#*] }"
    echo "    ${depth#*] }"
    case "$coding" in
        *"Rec. 2020 + SMPTE 2084 PQ"*) ok "the stream is HDR" ;;
        *) bad "the stream is SDR"
           warn "  if the client did not ask for HDR this is expected; check its settings"
           warn "  if it did, Sunshine downgraded it, and the reason is above this line" ;;
    esac
    case "$depth" in
        *10-bit*) ok "10 bit" ;;
        *) warn "not 10 bit, so it cannot be HDR whatever the coding says" ;;
    esac
fi

step "Anything Sunshine complained about"
log | grep -iE 'falling back to SDR|not supported|Couldn.t get display hdr metadata' | tail -5 | sed 's/^/    /'

step "Who is streaming, as the seat recorded it"
# polyseat-session writes SUNSHINE_CLIENT_HDR into this file, which is the
# client's side of the same question: what it asked for, rather than what it got.
insh "cat /home/$PLAYER/.local/share/polyseat/session.json 2>/dev/null" | sed 's/^/    /' \
    || warn "no session recorded"

step "The output, from sway"
# features.hdr is output_supports_hdr(), so it answers whether the patched
# wlroots is loaded; hdr is whether it is switched on. Both, because the two
# failures look identical from anywhere else.
as_player swaymsg -t get_outputs -r 2>/dev/null | python3 -c "
import json, sys
for o in json.load(sys.stdin):
    print('   ', o.get('name'), o.get('current_mode'),
          'supports hdr:', (o.get('features') or {}).get('hdr'),
          'hdr on:', o.get('hdr'))
" 2>/dev/null || warn "swaymsg did not answer"
