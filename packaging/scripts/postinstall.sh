#!/bin/sh
# What apt and dnf say after installing and after upgrading.
#
# The same two messages packaging/aur/polyseat.install prints, from one file,
# because dpkg and rpm do not have pacman's separate post_install and
# post_upgrade hooks: both call this and the script has to work out which
# happened. They say so in two different ways, which is the whole of the
# awkwardness below.
#
# Kept short on purpose, like the Arch one. This is the one place where nobody
# can click anything and nothing can be looked up later, so it carries the
# commands that cannot be discovered anywhere else and leaves the rest to the
# page.
set -e

# dpkg calls this with "configure" and, on an upgrade, the version that was
# there before. rpm calls it with the number of copies of this package that will
# be installed once the transaction finishes: 1 for a fresh install, 2 for an
# upgrade. Neither is guessable from the other, so both are read.
upgrade=false

case "${1:-}" in
    configure) [ -n "${2:-}" ] && upgrade=true ;;
    2)         upgrade=true ;;
esac

# polyseat_url prints the address to open, rather than a placeholder.
#
# The IPv4 address on the interface carrying the default route, which is the one
# another machine on this network can reach, and the host name when there is no
# route to ask. Printed on a line of its own with nothing after it, because that
# is what makes a terminal treat it as a link. There is a copy of this in
# packaging/aur/polyseat.install and in two shell scripts, which cannot source
# anything from each other: three lines in four places beats a library.
polyseat_url() {
    addr=$(ip -4 route get 1.1.1.1 2>/dev/null |
        awk '{for (i = 1; i <= NF; i++) if ($i == "src") {print $(i + 1); exit}}')

    [ -n "$addr" ] || addr=$(hostname 2>/dev/null)
    [ -n "$addr" ] || addr=this-machine

    printf 'https://%s:47800\n' "$addr"
}

if $upgrade; then
    cat <<MSG

Polyseat is upgraded on disk. The process that is running is still the old one:

  sudo systemctl restart polyseatd

Seats keep running through that. Each one's input broker restarts with the
daemon, so a controller can drop for a moment. If this version builds seats
differently, the page names the ones that are behind and has a button for them.

MSG
else
    cat <<MSG

Polyseat is installed. Start it:

  sudo systemctl enable --now polyseatd

Then open

  $(polyseat_url)

and choose a password. Nobody has claimed this machine yet, so whoever opens
that page first sets it, and everything else happens there. The first thing it
asks for is one press to prepare this host: the packages, Incus, the idmap
range and the driver check that a package is not allowed to do for you.

Both of those are commands as well, for anyone who would rather watch them:
polyseat-prepare and polyseat-uninstall.

MSG
fi

exit 0
