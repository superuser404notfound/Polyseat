#!/usr/bin/env bash
# H4 - the rule that hides seat pads from the KDE desktop.
# Writes nothing itself: prints the rule and the root command.
set -uo pipefail
source "$(dirname "$0")/lib.sh"

rule='# polyseat: the seats'"'"' virtual pads do not belong on the host desktop.
# Stripping ID_INPUT hides the device from every libinput compositor,
# LIBINPUT_IGNORE_DEVICE additionally hides it from libinput itself.
SUBSYSTEM=="input", ATTRS{name}=="polyseat:*", \
  ENV{ID_INPUT}="", ENV{ID_INPUT_JOYSTICK}="", ENV{LIBINPUT_IGNORE_DEVICE}="1"'

step "udev rule for /etc/udev/rules.d/70-polyseat-hide.rules"
echo "$rule"

cat <<EOF

Install it (as root):

  sudo tee /etc/udev/rules.d/70-polyseat-hide.rules >/dev/null <<'RULE'
$rule
RULE
  sudo udevadm control --reload
  # Restart the pad in 20-run-pad.sh once so the rule takes effect.

Then verify: the pad must no longer appear in the KDE system settings under
Game Controller, while 30-observe-host.sh still finds it.
EOF
