#!/bin/sh
# Polyseat - record who is streaming from this seat, and what.
#
#   polyseat-session          a client has connected
#   polyseat-session off      the stream has ended
#
# Written here rather than asked of Sunshine, because Sunshine does not answer
# it. Its API lists paired devices and serves its own log and configuration, and
# there is no endpoint for the session in progress: /api/session, /api/sessions,
# /api/status and /api/clients/active are all 404 on the version in a seat, and
# the log at its default level records the encoder and the bitrate and never the
# client. Checked rather than assumed.
#
# What is available is this: the commands in global_prep_cmd run when a stream
# starts and again when it ends, with the client's size, framerate and HDR in
# their environment, and the name of the application it asked for. That is most
# of the answer, so it is written down at the moment it is true.
#
# The client's address comes from the connection itself. Sunshine's control
# channel is a TCP connection that exists for as long as the stream does, so the
# peer on port 47989 or 48010 is the machine somebody is sitting at. Not a name:
# Moonlight only gives its name while pairing, and Sunshine keeps that against a
# certificate rather than against an address. The web interface lists the paired
# names beside this, which is as close as this can get without Sunshine's help.
#
# Never fails. This runs as a Sunshine prep command, and a prep command that
# returns non-zero stops the stream it was meant to describe.

: "${XDG_RUNTIME_DIR:=/run/user/$(id -u)}"

DIR=$HOME/.local/share/polyseat
FILE=$DIR/session.json

say() { echo "polyseat-session: $*" >&2; }

quote() {
    # Enough JSON escaping for an application name, which is the only value here
    # that somebody else chose.
    printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g'
}

if [ "$1" = "off" ]; then
    rm -f "$FILE" 2>/dev/null
    say "stream ended"
    exit 0
fi

mkdir -p "$DIR" 2>/dev/null || {
    say "cannot write $DIR, the interface will not show who is streaming"
    exit 0
}

width=${SUNSHINE_CLIENT_WIDTH:-}
height=${SUNSHINE_CLIENT_HEIGHT:-}
fps=${SUNSHINE_CLIENT_FPS:-}
hdr=${SUNSHINE_CLIENT_HDR:-}
app=${SUNSHINE_APP_NAME:-}

# The established control connection, if it can be seen. One address: a second
# client cannot stream from the same seat at the same time, so anything else on
# those ports is a client that has not finished connecting or has not finished
# going away.
# The last field rather than a numbered one: ss leaves the state column out when
# it has been asked for a single state, so the peer is the fourth column here and
# the fifth in the same command without the filter. Counting from the left gave
# an empty answer and looked exactly like nobody being connected.
peer=$(ss -Htn state established '( sport = :47989 or sport = :48010 )' 2>/dev/null |
    awk '{print $NF}' | sed 's/:[0-9]*$//; s/^\[//; s/\]$//' | head -1)

{
    printf '{'
    printf '"app":"%s"' "$(quote "$app")"
    [ -n "$width" ] && [ -n "$height" ] && printf ',"width":%s,"height":%s' "$width" "$height"
    [ -n "$fps" ] && printf ',"fps":%s' "$fps"
    [ -n "$hdr" ] && printf ',"hdr":"%s"' "$(quote "$hdr")"
    [ -n "$peer" ] && printf ',"peer":"%s"' "$(quote "$peer")"
    printf ',"started":"%s"' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    printf '}\n'
} > "$FILE.tmp" 2>/dev/null || {
    say "cannot write $FILE"
    exit 0
}

mv "$FILE.tmp" "$FILE" 2>/dev/null || rm -f "$FILE.tmp" 2>/dev/null

say "streaming ${app:-something} at ${width:-?}x${height:-?} to ${peer:-an unknown address}"

exit 0
