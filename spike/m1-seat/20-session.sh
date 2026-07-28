#!/usr/bin/env bash
# Sets up the seat session: PipeWire, headless Sway, Sunshine.
# All of it as the player's systemd user units, not as root.
set -euo pipefail
source "$(dirname "$0")/lib.sh"

PLAYER="${PLAYER:-player}"
UID_PLAYER=1000

# Running `systemctl --user` from outside: sudo needs `env`, and the user bus
# has to be named explicitly because there is no login context.
as_player() {
    incus exec "$CT" -- sudo -u "$PLAYER" env \
        XDG_RUNTIME_DIR="/run/user/$UID_PLAYER" \
        DBUS_SESSION_BUS_ADDRESS="unix:path=/run/user/$UID_PLAYER/bus" \
        SWAYSOCK="$(incus exec "$CT" -- sh -c "ls /run/user/1000/sway-ipc.*.sock 2>/dev/null | head -1")" \
        "$@"
}

step "Installing configuration and units"
incus exec "$CT" -- install -d -o "$PLAYER" -g "$PLAYER" \
    "/home/$PLAYER/.config/sway" \
    "/home/$PLAYER/.config/sunshine" \
    "/home/$PLAYER/.config/systemd/user"

incus file push "$HERE/files/sway.config" \
    "$CT/home/$PLAYER/.config/sway/config"
incus file push "$HERE/files/sunshine.conf" \
    "$CT/home/$PLAYER/.config/sunshine/sunshine.conf"

# By default Sunshine's CSRF protection only allows localhost origins. Opening
# the web UI through the LAN or management address, which is what always
# happens, otherwise fails on save with "CSRF Protection Error". The allowed
# origins are therefore generated from the seat's actual addresses.
origins=""
for iface in eth0 eth1; do
    ip=$(incus exec "$CT" -- sh -c \
        "ip -4 -o addr show $iface 2>/dev/null | awk '{print \$4}' | cut -d/ -f1")
    [[ -n "$ip" ]] && origins="${origins:+$origins,}https://$ip:47990"
done
[[ -n "$origins" ]] && incus exec "$CT" -- sh -c \
    "printf 'csrf_allowed_origins = %s\n' '$origins' >> /home/$PLAYER/.config/sunshine/sunshine.conf"
ok "csrf_allowed_origins = $origins"

incus file push "$HERE/files/polyseat-sway.service" \
    "$CT/home/$PLAYER/.config/systemd/user/polyseat-sway.service"
incus file push "$HERE/files/polyseat-sunshine.service" \
    "$CT/home/$PLAYER/.config/systemd/user/polyseat-sunshine.service"
incus file push "$HERE/files/sunshine-run.sh" \
    "$CT/usr/local/bin/polyseat-sunshine-run" --mode 0755

# Seat tag inside the device names.
#
# Sunshine reads XDG_SEAT and appends the seat name to its virtual input
# devices as soon as the seat is not "seat0": "Keyboard passthrough" becomes
# "Keyboard passthrough (seat1)". That is exactly how the broker later tells
# which seat a device belongs to. Without it every seat's devices carry
# identical names and cannot be assigned.
#
# Generated as a drop-in because the value differs per seat.
incus exec "$CT" -- install -d -o "$PLAYER" -g "$PLAYER" \
    "/home/$PLAYER/.config/systemd/user/polyseat-sunshine.service.d"
incus exec "$CT" -- sh -c "printf '[Service]\nEnvironment=XDG_SEAT=%s\n' '$CT' \
    > /home/$PLAYER/.config/systemd/user/polyseat-sunshine.service.d/10-seat.conf"
incus exec "$CT" -- chown -R "$PLAYER:$PLAYER" "/home/$PLAYER/.config"

step "mDNS (so Moonlight finds the seat by itself)"
incus exec "$CT" -- systemctl enable --now avahi-daemon >/dev/null 2>&1 \
    && ok "avahi running" || warn "avahi could not be started"

step "Starting the audio stack"
as_player systemctl --user daemon-reload
as_player systemctl --user enable --now pipewire.socket pipewire-pulse.socket wireplumber.service \
    >/dev/null 2>&1 || warn "PipeWire units reported an error"
sleep 2
as_player pactl info 2>&1 | head -3 || warn "pactl does not answer"

step "Starting the Sway session"
as_player systemctl --user enable --now polyseat-sway.service
sleep 5

step "Status"
as_player systemctl --user --no-pager is-active polyseat-sway.service \
    && ok "sway is running" || bad "sway is not running"
as_player systemctl --user --no-pager is-active polyseat-sunshine.service \
    && ok "sunshine is running" || bad "sunshine is not running"

echo
echo "Next: ./30-verify.sh"
