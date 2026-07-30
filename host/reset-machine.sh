#!/usr/bin/env bash
# Puts this machine back to before Polyseat was ever installed, for testing the
# installer from scratch.
#
#   sudo ./reset-machine.sh              keep the shared game library
#   sudo ./reset-machine.sh --library    remove that too
#
# --yes answers the question.
#
# This is a development tool and not part of uninstalling Polyseat. That is
# install.sh --uninstall, which leaves the seats, and install.sh --purge, which
# takes them and deliberately leaves the packages and Incus alone: they are not
# Polyseat's to remove. This script removes them anyway, because the first thing
# a new user does is run the installer on a machine that has none of it, and that
# is the one path a developed-on machine can never test by accident.
#
# It exists because doing it by hand went wrong twice in the same afternoon. The
# first time an `incus delete` hung for ten minutes, because the daemon was still
# reading inside the container; --purge fixed the order. The second time
# `rm -rf /var/lib/incus` left two thirds of it behind: btrfs refuses to delete a
# subvolume that way, and three tmpfs mounts were still in place. Both are handled
# below.
#
# What it does NOT touch: the graphics driver. The installer requires it, installing
# one is not this script's business, and a machine without graphics is a poor
# starting point for testing anything.
set -euo pipefail

HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
LIBRARYDIR=/srv/polyseat/library

# The packages the installer installs. Deliberately not python: it is a
# dependency of half the system.
PACKAGES=(incus nvidia-container-toolkit bpftrace go)

ok()   { printf '  \033[32m✓\033[0m %s\n' "$*"; }
warn() { printf '  \033[33m!\033[0m %s\n' "$*"; }
step() { printf '\n\033[1m%s\033[0m\n' "$*"; }

[[ $EUID -eq 0 ]] || { echo "needs root"; exit 1; }

library=false
assume_yes=false

for arg in "$@"; do
    case "$arg" in
        --library) library=true ;;
        --yes) assume_yes=true ;;
        *) echo "unknown option: $arg"; exit 1 ;;
    esac
done

step "This resets the machine to before Polyseat"
echo "  Polyseat and its seats:  removed, by install.sh --purge"
echo "  packages:                ${PACKAGES[*]}"
echo "  Incus state:             /var/lib/incus, including its images and pools"
echo "  machine settings:        the root idmap ranges, and this account's input group"
if $library; then
    echo "  shared game library:     $LIBRARYDIR, and its games with it"
else
    echo "  shared game library:     $LIBRARYDIR is KEPT, so the games come back"
fi
echo "  graphics driver:         left alone, the installer needs it"
echo

if ! $assume_yes; then
    read -r -p "  Type reset to go ahead: " answer
    [[ $answer == "reset" ]] || { echo "  nothing done"; exit 1; }
fi

step "Polyseat, in the order that works"
purge=("$HERE/install.sh" --purge --yes)
$library && purge+=(--library)

# Not piped anywhere. sed buffers when its output is not a terminal, so piping
# this for the sake of indentation hid every line of a step that can take minutes
# and made a working run look like a hung one.
"${purge[@]}"

step "Packages"
# Incus stopped before its package goes, or the running daemon outlives it: the
# first run left an incusd, two dnsmasq children and two bridges behind, all
# belonging to a package that was no longer installed.
systemctl stop incus.service incus.socket >/dev/null 2>&1 || true
pkill -f "/usr/lib/incus/incusd" 2>/dev/null || true
pkill -f "dnsmasq.*incusbr" 2>/dev/null || true

installed=()
for p in "${PACKAGES[@]}"; do
    pacman -Qq "$p" >/dev/null 2>&1 && installed+=("$p")
done

if ((${#installed[@]})); then
    # -R and not -Rns: taking dependencies with them means downloading half a
    # toolchain again on the next install for no gain.
    pacman -R --noconfirm "${installed[@]}" >/dev/null
    ok "removed ${installed[*]}"
else
    ok "none of them were installed"
fi

step "Incus state"
# The subvolumes first, children before parents, because btrfs refuses to delete
# a subvolume that still has any. rm cannot do it at all, which is how this was
# left two thirds full the first time.
while read -r path; do
    [[ -n $path ]] || continue
    btrfs subvolume delete "/$path" >/dev/null 2>&1 || true
done < <(btrfs subvolume list / 2>/dev/null | awk '/var\/lib\/incus/ {print $NF}' | sort -r)

# Then the mounts incusd leaves behind. rm reports "device or resource busy" for
# these and carries on, which looks like success.
while read -r m; do
    [[ -n $m ]] || continue
    umount "$m" 2>/dev/null || umount -l "$m" 2>/dev/null || true
done < <(mount | awk '/\/var\/lib\/incus/ {print $3}' | sort -r)

rm -rf /var/lib/incus

if [[ -e /var/lib/incus ]]; then
    warn "/var/lib/incus is still there:"
    du -sh /var/lib/incus | sed 's/^/    /'
else
    ok "/var/lib/incus is gone"
fi

step "Network left behind"
# The bridges outlive the daemon and the package, and a stale incusbr0 is exactly
# the sort of thing that makes the next install look haunted.
removed_links=0
while read -r link; do
    [[ -n $link ]] || continue
    ip link delete "$link" >/dev/null 2>&1 && removed_links=$((removed_links + 1))
done < <(ip -o link show | awk -F': ' '/incusbr/ {print $2}' | cut -d@ -f1)

((removed_links)) && ok "removed $removed_links leftover incusbr interface(s)" ||
    ok "no leftover bridges"

step "Machine settings the installer puts back"
sed -i '/^root:1000000:1000000000$/d' /etc/subuid /etc/subgid 2>/dev/null || true
ok "root idmap ranges removed"

target_user=${SUDO_USER:-}
if [[ -n $target_user && $target_user != root ]]; then
    gpasswd -d "$target_user" input >/dev/null 2>&1 &&
        ok "$target_user removed from the input group, which its running shells keep until they exit" ||
        ok "$target_user was not in the input group"
fi

step "What is left"
for p in /usr/local/bin/polyseatd /usr/local/lib/polyseat \
         /etc/systemd/system/polyseatd.service /var/lib/polyseat /var/lib/incus; do
    printf '  %-44s %s\n' "$p" "$([[ -e $p ]] && echo PRESENT || echo gone)"
done

if [[ -d $LIBRARYDIR ]]; then
    printf '  %-44s %s\n' "$LIBRARYDIR" "$(du -sh "$LIBRARYDIR" | cut -f1), kept"
fi

if driver=$(nvidia-smi --query-gpu=driver_version --format=csv,noheader 2>/dev/null); then
    printf '  %-44s %s\n' "NVIDIA driver" "$driver, still there"
fi

cat <<EOF

Ready. Install it again with:

  sudo $HERE/install.sh
  sudo systemctl enable --now polyseatd
EOF
