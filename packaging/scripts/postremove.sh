#!/bin/sh
# What apt and dnf say after removing.
#
# Not printed during an upgrade, which is the one thing this has to get right:
# both package managers call this while replacing the old version with the new
# one, and telling somebody their seats are still there in the middle of an
# upgrade they did not think of as a removal is noise at best.
#
# dpkg says "upgrade" as the first argument in that case. rpm says how many
# copies remain, which is 1 during an upgrade and 0 when the package is really
# going.
set -e

case "${1:-}" in
    upgrade|failed-upgrade|1) exit 0 ;;
esac

if systemctl is-active --quiet polyseatd.service 2>/dev/null; then
    cat <<MSG

polyseatd is still running, from a binary that is no longer on disk. Removing a
package does not stop a service:

  sudo systemctl stop polyseatd

MSG
fi

cat <<MSG

Two things are left behind, because they are yours and not the package's:

  /var/lib/polyseat   the seats, their pairings and the interface password
  /srv/polyseat       the shared game library

The seats are still Incus containers, and installing Polyseat again picks them
up where they were. To have taken them along, polyseat-uninstall --seats was
the command, and it has just gone with the package:

  sudo incus delete -f <seat>
  sudo rm -rf /var/lib/polyseat /etc/polyseat

MSG

exit 0
