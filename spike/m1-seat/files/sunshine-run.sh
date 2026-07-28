#!/bin/sh
# Startet Sunshine erst, wenn die Wayland-Session wirklich bereit ist.
#
# Ohne das gibt es eine Race Condition mit hässlicher Nachwirkung: Startet
# Sunshine, bevor sways Socket existiert, meldet es
#     [wayland] Couldn't connect to Wayland display
#     Platform failed to initialize
# und danach scheitern ALLE Encoder — auch der Software-Encoder, was die
# Ursache gut verschleiert. Sunshine beendet sich dabei nicht, sondern läuft
# kaputt weiter und bedient die Weboberfläche. systemd sieht einen gesunden
# Dienst, der Client bekommt "Failed to initialize video capture/encoding.
# Is a display connected and turned on?".
#
# Deshalb wird der Socket hier selbst gesucht, statt sich auf ein per
# `systemctl --user import-environment` gesetztes WAYLAND_DISPLAY zu
# verlassen — das kann nach einem sway-Neustart veraltet sein.
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
    echo "polyseat: kein Wayland-Socket in $XDG_RUNTIME_DIR gefunden" >&2
    exit 1
fi

# Der Socket entsteht, bevor sway Verbindungen beantwortet.
sleep 1

WAYLAND_DISPLAY="${sock##*/}"
export WAYLAND_DISPLAY
echo "polyseat: Sunshine startet auf $WAYLAND_DISPLAY"
exec /usr/bin/sunshine "$@"
