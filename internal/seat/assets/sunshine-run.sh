#!/bin/sh
# Starts Sunshine only once the Wayland session is really up.
#
# Without this there is a race with an ugly aftermath: if Sunshine starts
# before sway's socket exists, it reports
#     [wayland] Couldn't connect to Wayland display
#     Platform failed to initialize
# and afterwards ALL encoders fail - including the software encoder, which
# hides the cause nicely. Sunshine does not exit in that state; it keeps
# running broken and serves the web UI. systemd sees a healthy service, and the
# client only gets "Failed to initialize video capture/encoding. Is a display
# connected and turned on?".
#
# That is why the socket is looked up here instead of relying on a
# WAYLAND_DISPLAY set through `systemctl --user import-environment` - that value
# can be stale after a sway restart.
: "${XDG_RUNTIME_DIR:=/run/user/$(id -u)}"

sock=""
i=0
while [ "$i" -lt 150 ]; do
    sock=$(ls -t "$XDG_RUNTIME_DIR"/wayland-[0-9]* 2>/dev/null | grep -v '\.lock$' | head -1)
    if [ -n "$sock" ] && [ -S "$sock" ]; then
        break
    fi
    sock=""
    i=$((i + 1))
    sleep 0.2
done

if [ -z "$sock" ]; then
    echo "polyseat: no Wayland socket found in $XDG_RUNTIME_DIR" >&2
    exit 1
fi

# The socket appears before sway starts answering connections.
sleep 1

WAYLAND_DISPLAY="${sock##*/}"
export WAYLAND_DISPLAY
echo "polyseat: starting Sunshine on $WAYLAND_DISPLAY"
exec /usr/bin/sunshine "$@"
