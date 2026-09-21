#!/bin/sh
# Polyseat - have Steam already running when somebody sits down.
#
# Started by the session and also by the daemon, which is the whole reason this
# is a script rather than a line in the sway configuration. The daemon closes an
# idle Steam when it has to change something Steam has already read, and it has
# to be able to put back exactly what the session would have started.
#
# Silent, and not Big Picture. Measured in two seats on this machine, summed PSS
# rather than RSS so that the pages the sixteen processes share are counted once:
#
#     steam -silent, no Big Picture       16 processes     732 MB
#     Steam with Big Picture open         18 processes    1324 MB
#
# and Big Picture also renders its interface for as long as it is open, which in
# an autostarted seat would be for as long as the host is switched on. Silent
# costs the memory and almost no CPU, and that is the part somebody waits for:
# a cold Steam here takes 15 to 25 seconds before it shows anything, and every
# one of those seconds is spent in front of a player who has just picked a game.
#
# What this does not do is save that wait twice. Steam stays alive after a
# stream ends - the Big Picture entry closes Big Picture and not the client - so
# the wait was already once per seat boot. This moves it off the player and on
# to the boot.
#
# Never fails, and never waits. It is an exec line in a session that has other
# things to start, and a Steam that cannot start is a seat that still streams.

: "${XDG_RUNTIME_DIR:=/run/user/$(id -u)}"
: "${DISPLAY:=:0}"
export XDG_RUNTIME_DIR DISPLAY

say() { echo "polyseat-steam: $*" >&2; }

# Asked rather than assumed, because the daemon calls this after it has closed
# one and the session calls it at startup, and a second client on the same home
# directory is not a thing Steam handles by ignoring it: it hands the first one
# the new arguments and leaves a confusing line in the log.
if pgrep -x steam >/dev/null 2>&1; then
    say "Steam is already running, so nothing was started"

    exit 0
fi

say "starting Steam in the background"

setsid steam -silent >/dev/null 2>&1 </dev/null &

exit 0
