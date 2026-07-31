#!/bin/sh
# Polyseat - what the first terminal in a seat shows.
#
#   polyseat-welcome           print this and hand over to a shell
#   polyseat-welcome --print   print it and stop, for looking it up again
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
#
# The gamepad section is the longest on purpose. It is the one set of controls
# nothing else in the seat announces: a keybinding can be discovered by trying
# Super and a letter, and a button on a pad cannot.

guide() {
    printf '\n'
    printf '  Polyseat seat: %s\n' "${XDG_SEAT:-unknown}"
    printf '  ---------------------------------------------------------------\n'
    printf '\n'
    printf '  Keys        Super+D       open the app launcher\n'
    printf '              Super+Return  a new terminal\n'
    printf '              Super+E       the file manager\n'
    printf '              Super+K       show or hide the on-screen keyboard\n'
    printf '              Super+Q       close the focused window\n'
    printf '              Super+F       fullscreen\n'
    printf '              Super+1..4    switch workspace\n'
    printf '\n'
    printf '  Gamepad     Pointer mode follows what is in front. On the\n'
    printf '              desktop it is on; a fullscreen application takes the\n'
    printf '              controller back, so a game stays a game.\n'
    printf '\n'
    printf '              Select + Start        hold both for a second to\n'
    printf '                                    override it by hand, and the\n'
    printf '                                    pad buzzes. The override\n'
    printf '                                    holds until something goes\n'
    printf '                                    fullscreen or stops being.\n'
    printf '                                    Any two of Select, Start and\n'
    printf '                                    Guide do it, because some\n'
    printf '                                    clients send Guide for the\n'
    printf '                                    pair whatever you press.\n'
    printf '\n'
    printf '              left stick            move the pointer\n'
    printf '              right stick           scroll\n'
    printf '              A                     left click\n'
    printf '              X                     right click\n'
    printf '              B                     Escape\n'
    printf '              Y                     Enter\n'
    printf '              Start                 Enter\n'
    printf '                                    (a short press. Holding Start\n'
    printf '                                    is the chord above)\n'
    printf '              LB                    Backspace\n'
    printf '              RB                    Tab\n'
    printf '              D-pad                 arrow keys\n'
    printf '              L3  (press left)      the on-screen keyboard\n'
    printf '              R3  (press right)     middle click\n'
    printf '\n'
    printf '              That is enough to get through a login form without\n'
    printf '              a keyboard: point at a field, press A, then L3.\n'
    printf '\n'
    printf '  Games       Steam and Lutris are installed. Pick "Steam Big\n'
    printf '              Picture" in Moonlight to go straight there, or\n'
    printf '              open either from the launcher. Installed games are\n'
    printf '              in both menus by themselves, so a game is one pick\n'
    printf '              away without opening a launcher at all.\n'
    printf '\n'

    if [ -d "$HOME/games" ]; then
        printf '  Library     %s is the shared library. A game installed\n' "$HOME/games"
        printf '              there shows up in every other seat by itself.\n'
        printf '\n'
    fi

    printf '  Install     Open "Software" from the launcher and browse\n'
    printf '              Flathub, or install into this seat from the\n'
    printf '              Polyseat page on your phone. Neither needs a\n'
    printf '              password. On the command line:\n'
    printf '                  flatpak install flathub <application id>\n'
    printf '                  flatpak search <name>\n'
    printf '\n'
    printf '  AppImage    For what is published no other way, emulators\n'
    printf '              mostly. Download it with Firefox and leave it in\n'
    printf '              %s: it moves itself into\n' "$HOME/Downloads"
    printf '              %s within a minute and turns up\n' "$HOME/Applications"
    printf '              in the launcher and in Moonlight. The Polyseat page\n'
    printf '              can also fetch one from an address.\n'
    printf '\n'
    printf '  ---------------------------------------------------------------\n'
    printf '  Run polyseat-welcome --print to see this again.\n'
    printf '\n'
}

guide

if [ "$1" = "--print" ]; then
    exit 0
fi

# The shell to hand over to. Falls back rather than failing: the seat image is
# minimal and this file should not be the reason a terminal is unusable.
shell=${SHELL:-/bin/bash}
[ -x "$shell" ] || shell=/bin/sh

exec "$shell"
