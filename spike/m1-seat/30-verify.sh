#!/usr/bin/env bash
# Checks the finished seat and prints what is needed to connect.
set -uo pipefail
source "$(dirname "$0")/lib.sh"

PLAYER="${PLAYER:-player}"
# swaymsg only finds the IPC socket through SWAYSOCK, and without a login
# context that variable is unset, so it gets resolved here.
SWAYSOCK_PATH="$(incus exec "$CT" -- sh -c \
    'ls /run/user/1000/sway-ipc.*.sock 2>/dev/null | head -1')"

as_player() {
    incus exec "$CT" -- sudo -u "$PLAYER" env \
        XDG_RUNTIME_DIR=/run/user/1000 \
        DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus \
        SWAYSOCK="$SWAYSOCK_PATH" "$@"
}

step "Services"
for u in polyseat-sway polyseat-sunshine; do
    s=$(as_player systemctl --user is-active "$u.service" 2>/dev/null)
    [[ "$s" == active ]] && ok "$u: $s" || bad "$u: $s"
done

step "GPU and encoder"
incus exec "$CT" -- nvidia-smi -L 2>/dev/null | grep -q '^GPU' \
    && ok "GPU visible" || bad "GPU missing"
enc=$(as_player journalctl --user -u polyseat-sunshine.service --no-pager 2>/dev/null \
      | grep -oE 'Found H\.264 encoder: [a-z0-9_]+ \[[a-z]+\]' | tail -1)
case "$enc" in
    *nvenc*)    ok "$enc" ;;
    *software*) bad "$enc  (hardware encoding is not being used!)" ;;
    *)          warn "no encoder entry found in the log" ;;
esac

step "Display"
as_player swaymsg -t get_outputs 2>/dev/null | grep -E '"name"|"width"|"height"' | head -4 \
    || warn "swaymsg not reachable (SWAYSOCK?)"

step "Audio"
as_player pactl list sinks short 2>/dev/null || warn "PipeWire does not answer"

step "Network"
echo "  LAN (for Moonlight):"
incus exec "$CT" -- ip -4 -br addr show eth1 2>/dev/null | sed 's/^/    /'
echo "  Management (reachable from the host, the LAN address is not):"
incus exec "$CT" -- ip -4 -br addr show eth0 2>/dev/null | sed 's/^/    /'

lan=$(incus exec "$CT" -- sh -c "ip -4 -o addr show eth1 | awk '{print \$4}' | cut -d/ -f1" 2>/dev/null)
mgmt=$(incus exec "$CT" -- sh -c "ip -4 -o addr show eth0 | awk '{print \$4}' | cut -d/ -f1" 2>/dev/null)

cat <<EOF

How to connect:

  1. Open the Sunshine web UI in a browser on the host:
         https://${mgmt}:47990
     (through the management address, because the LAN address is NOT
     reachable from the host due to macvlan.) On first visit, set a
     username and password.

  2. In Moonlight on the client, add the host:
         ${lan}
     The seat also announces itself via mDNS as "${CT}".

  3. Moonlight shows a PIN. Enter it in the web UI under "PIN".
EOF
