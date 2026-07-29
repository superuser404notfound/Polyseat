#!/bin/sh
# Polyseat - what the first terminal in a seat shows.
#
# This exists because of a specific dead end. A seat used to come up as a bare
# sway session with one terminal in it and nothing else: no launcher, no bar,
# no hint. Somebody connecting for the first time could see that it worked and
# still had no way to start Steam or install anything, because every route to
# doing so was a keybinding or a command they had no reason to guess.
#
# So the answers are on the screen. Printed rather than shown in a help window
# because a terminal is the one thing that is definitely already there, and
# because whoever reads it is one keystroke away from acting on it.

# The shell to hand over to afterwards. Falls back rather than failing: the
# seat image is minimal and this file should not be the reason a terminal is
# unusable.
shell=${SHELL:-/bin/bash}
[ -x "$shell" ] || shell=/bin/sh

printf '\n'
printf '  Polyseat seat: %s\n' "${XDG_SEAT:-unknown}"
printf '  ---------------------------------------------------------------\n'
printf '\n'
printf '  Keys        Super+D       open the app launcher\n'
printf '              Super+Return  a new terminal\n'
printf '              Super+E       the file manager\n'
printf '              Super+Q       close the focused window\n'
printf '              Super+F       fullscreen\n'
printf '              Super+1..4    switch workspace\n'
printf '\n'
printf '  Games       Steam is installed. Pick "Steam Big Picture" in\n'
printf '              Moonlight to go straight there, or run: steam\n'
printf '\n'

if [ -d "$HOME/games" ]; then
    printf '  Library     %s is the shared library. A game installed\n' "$HOME/games"
    printf '              there shows up in every other seat by itself.\n'
    printf '\n'
fi

printf '  Install     flatpak install flathub com.heroicgameslauncher.hgl\n'
printf '              flatpak install flathub net.lutris.Lutris\n'
printf '              flatpak search <name>      to look something up\n'
printf '\n'
printf '              These need no password. Anything installed this way\n'
printf '              appears in the launcher, and in the Moonlight app list\n'
printf '              the next time the seat starts.\n'
printf '\n'
printf '  ---------------------------------------------------------------\n'
printf '\n'

exec "$shell"
