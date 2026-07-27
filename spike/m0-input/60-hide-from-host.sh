#!/usr/bin/env bash
# H4 — die Regel, die Seat-Pads vor dem KDE-Desktop versteckt.
# Schreibt nichts selbst: druckt die Regel und den Root-Befehl.
set -uo pipefail
source "$(dirname "$0")/lib.sh"

rule='# polyseat: virtuelle Pads der Seats gehören nicht auf den Host-Desktop.
# ID_INPUT-Strip versteckt das Gerät vor allen libinput-Compositoren,
# LIBINPUT_IGNORE_DEVICE zusätzlich vor libinput selbst.
SUBSYSTEM=="input", ATTRS{name}=="polyseat:*", \
  ENV{ID_INPUT}="", ENV{ID_INPUT_JOYSTICK}="", ENV{LIBINPUT_IGNORE_DEVICE}="1"'

step "udev-Regel für /etc/udev/rules.d/70-polyseat-hide.rules"
echo "$rule"

cat <<EOF

Installieren (root):

  sudo tee /etc/udev/rules.d/70-polyseat-hide.rules >/dev/null <<'RULE'
$rule
RULE
  sudo udevadm control --reload
  # Pad in 20-run-pad.sh einmal neu starten, damit die Regel greift.

Danach prüfen: das Pad darf in den KDE-Systemeinstellungen unter
Gamecontroller nicht mehr auftauchen, in 30-observe-host.sh aber schon.
EOF
