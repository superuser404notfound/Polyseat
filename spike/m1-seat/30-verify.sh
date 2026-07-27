#!/usr/bin/env bash
# Prüft den fertigen Seat und druckt, was zum Verbinden gebraucht wird.
set -uo pipefail
source "$(dirname "$0")/lib.sh"

PLAYER="${PLAYER:-player}"
# swaymsg findet den IPC-Socket nur über SWAYSOCK — ohne Login-Kontext ist
# die Variable nicht gesetzt, also wird sie hier aufgelöst.
SWAYSOCK_PATH="$(incus exec "$CT" -- sh -c \
    'ls /run/user/1000/sway-ipc.*.sock 2>/dev/null | head -1')"

as_player() {
    incus exec "$CT" -- sudo -u "$PLAYER" env \
        XDG_RUNTIME_DIR=/run/user/1000 \
        DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus \
        SWAYSOCK="$SWAYSOCK_PATH" "$@"
}

step "Dienste"
for u in polyseat-sway polyseat-sunshine; do
    s=$(as_player systemctl --user is-active "$u.service" 2>/dev/null)
    [[ "$s" == active ]] && ok "$u: $s" || bad "$u: $s"
done

step "GPU und Encoder"
incus exec "$CT" -- nvidia-smi -L 2>/dev/null | grep -q '^GPU' \
    && ok "GPU sichtbar" || bad "GPU fehlt"
enc=$(as_player journalctl --user -u polyseat-sunshine.service --no-pager 2>/dev/null \
      | grep -oE 'Found H\.264 encoder: [a-z0-9_]+ \[[a-z]+\]' | tail -1)
case "$enc" in
    *nvenc*)    ok "$enc" ;;
    *software*) bad "$enc  — Hardware-Encoding greift nicht!" ;;
    *)          warn "kein Encoder-Eintrag im Log gefunden" ;;
esac

step "Anzeige"
as_player swaymsg -t get_outputs 2>/dev/null | grep -E '"name"|"width"|"height"' | head -4 \
    || warn "swaymsg nicht erreichbar (SWAYSOCK?)"

step "Audio"
as_player pactl list sinks short 2>/dev/null || warn "PipeWire antwortet nicht"

step "Netzwerk"
echo "  LAN (für Moonlight):"
incus exec "$CT" -- ip -4 -br addr show eth1 2>/dev/null | sed 's/^/    /'
echo "  Verwaltung (vom Host erreichbar, macvlan ist es nicht):"
incus exec "$CT" -- ip -4 -br addr show eth0 2>/dev/null | sed 's/^/    /'

lan=$(incus exec "$CT" -- sh -c "ip -4 -o addr show eth1 | awk '{print \$4}' | cut -d/ -f1" 2>/dev/null)
mgmt=$(incus exec "$CT" -- sh -c "ip -4 -o addr show eth0 | awk '{print \$4}' | cut -d/ -f1" 2>/dev/null)

cat <<EOF

So verbindest du dich:

  1. Sunshine-Weboberfläche im Browser des Hosts öffnen:
         https://${mgmt}:47990
     (über die Verwaltungsadresse — die LAN-Adresse ist vom Host aus
     wegen macvlan NICHT erreichbar.) Beim ersten Aufruf Benutzername
     und Passwort setzen.

  2. In Moonlight auf dem Client den Host hinzufügen:
         ${lan}
     Der Seat meldet sich auch per mDNS als "${CT}".

  3. Moonlight zeigt eine PIN. Die in der Weboberfläche unter "PIN"
     eintragen.
EOF
