#!/usr/bin/env bash
# Richtet die Seat-Session ein: PipeWire, headless Sway, Sunshine —
# alles als systemd-User-Units des Spielers, nicht als root.
set -euo pipefail
source "$(dirname "$0")/lib.sh"

PLAYER="${PLAYER:-player}"
UID_PLAYER=1000

# systemctl --user von außen: sudo braucht `env`, und der User-Bus muss
# explizit benannt werden, weil kein Login-Kontext existiert.
as_player() {
    incus exec "$CT" -- sudo -u "$PLAYER" env \
        XDG_RUNTIME_DIR="/run/user/$UID_PLAYER" \
        DBUS_SESSION_BUS_ADDRESS="unix:path=/run/user/$UID_PLAYER/bus" \
        SWAYSOCK="$(incus exec "$CT" -- sh -c "ls /run/user/1000/sway-ipc.*.sock 2>/dev/null | head -1")" \
        "$@"
}

step "Konfiguration und Units einspielen"
incus exec "$CT" -- install -d -o "$PLAYER" -g "$PLAYER" \
    "/home/$PLAYER/.config/sway" \
    "/home/$PLAYER/.config/sunshine" \
    "/home/$PLAYER/.config/systemd/user" \
    "/home/$PLAYER/.config/pipewire/pipewire.conf.d"

incus file push "$HERE/files/10-polyseat-sink.conf" \
    "$CT/home/$PLAYER/.config/pipewire/pipewire.conf.d/10-polyseat-sink.conf"

incus file push "$HERE/files/sway.config" \
    "$CT/home/$PLAYER/.config/sway/config"
incus file push "$HERE/files/sunshine.conf" \
    "$CT/home/$PLAYER/.config/sunshine/sunshine.conf"

# Sunshines CSRF-Schutz erlaubt von Haus aus nur localhost-Ursprünge. Wer die
# Weboberfläche über die LAN- oder Verwaltungsadresse öffnet — also immer —
# bekommt sonst beim Speichern "CSRF Protection Error". Die erlaubten
# Ursprünge werden deshalb aus den tatsächlichen Adressen des Seats erzeugt.
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
incus exec "$CT" -- chown -R "$PLAYER:$PLAYER" "/home/$PLAYER/.config"

step "mDNS (damit Moonlight den Seat von selbst findet)"
incus exec "$CT" -- systemctl enable --now avahi-daemon >/dev/null 2>&1 \
    && ok "avahi läuft" || warn "avahi ließ sich nicht starten"

step "Audio-Stack starten"
as_player systemctl --user daemon-reload
as_player systemctl --user enable --now pipewire.socket pipewire-pulse.socket wireplumber.service \
    >/dev/null 2>&1 || warn "PipeWire-Units meldeten einen Fehler"
sleep 2
as_player pactl info 2>&1 | head -3 || warn "pactl antwortet nicht"

step "Sway-Session starten"
as_player systemctl --user enable --now polyseat-sway.service
sleep 5

step "Status"
as_player systemctl --user --no-pager is-active polyseat-sway.service \
    && ok "sway läuft" || bad "sway läuft nicht"
as_player systemctl --user --no-pager is-active polyseat-sunshine.service \
    && ok "sunshine läuft" || bad "sunshine läuft nicht"

echo
echo "Weiter mit: ./30-verify.sh"
