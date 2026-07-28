#!/usr/bin/env bash
# Creates the complete seat container - up to just before the session starts.
#
# The order is deliberate: packages BEFORE enabling nvidia.runtime, otherwise
# pacman collides with the injected driver files (finding from M0).
set -euo pipefail
source "$(dirname "$0")/lib.sh"

PLAYER="${PLAYER:-player}"
UPLINK="${UPLINK:-enp4s0}"

if incus info "$CT" >/dev/null 2>&1; then
    warn "Container '$CT' already exists - run 99-cleanup.sh first."
    exit 1
fi

wait_boot() {
    # `incus launch` returns as soon as the container is running - not as soon
    # as its systemd is ready. Without this wait the first systemctl fails with
    # "Failed to connect to system scope bus".
    for _ in $(seq 90); do
        incus exec "$CT" -- systemctl is-system-running 2>/dev/null \
            | grep -qE 'running|degraded' && return 0
        sleep 1
    done
    bad "systemd inside the container never became ready"
    return 1
}

step "Creating container '$CT' (still without NVIDIA)"
incus launch images:archlinux/current "$CT"
wait_boot

step "Second network interface: macvlan into the LAN"
# eth0 stays on incusbr0 and is the management path (the daemon will later talk
# to the seat through it). eth1 sits directly in the LAN via macvlan so that
# Moonlight sees the seat as a host of its own - every seat gets its own address
# that way and can use the standard Sunshine ports.
# Known property of macvlan: host and container CANNOT reach each other over
# this interface. That is exactly what eth0 is for.
incus config device add "$CT" eth1 nic nictype=macvlan parent="$UPLINK" name=eth1

step "Setting up DHCP for eth1 inside the container"
incus exec "$CT" -- sh -c 'cat > /etc/systemd/network/50-eth1.network <<EOF
[Match]
Name=eth1

[Network]
DHCP=yes
EOF
systemctl restart systemd-networkd'

step "Waiting for networking"
for _ in $(seq 60); do
    incus exec "$CT" -- getent hosts geo.mirror.pkgbuild.com >/dev/null 2>&1 && break
    sleep 1
done

step "Providing the CachyOS repository inside the container"
# Sunshine does not live in the Arch repositories but in the CachyOS one.
# Rather than building it from the AUR, only the *non* CPU-optimised [cachyos]
# repo is added - and at the END of pacman.conf, so that Arch wins for every
# shared package and the container stays an Arch container.
for p in cachyos-keyring cachyos-mirrorlist; do
    f=$(ls -t /var/cache/pacman/pkg/$p-*.pkg.tar.zst | head -1)
    incus file push "$f" "$CT/root/$(basename "$f")"
    incus exec "$CT" -- pacman -U --noconfirm "/root/$(basename "$f")"
done
# The mirror list shipped by the package starts with mirrors that serve stale
# indexes; the host's already sorted list is more reliable.
incus file push /etc/pacman.d/cachyos-mirrorlist "$CT/etc/pacman.d/cachyos-mirrorlist"
incus exec "$CT" -- sh -c 'grep -q "^\[cachyos\]" /etc/pacman.conf || printf "\n[cachyos]\nInclude = /etc/pacman.d/cachyos-mirrorlist\n" >> /etc/pacman.conf'
incus exec "$CT" -- pacman-key --populate cachyos

step "Installing packages"
incus exec "$CT" -- pacman -Syu --noconfirm --needed \
    sway swaybg foot xorg-xwayland \
    sunshine avahi \
    pipewire pipewire-pulse pipewire-audio wireplumber \
    mesa vulkan-tools mesa-utils \
    python sudo which

step "Installing Steam and the 32-bit userspace"
# Steam belongs in the base installation, not in a later add-on install: once
# nvidia.runtime has been enabled the injection leaves real symlinks behind in
# the container filesystem, and those never go away again, so lib32-mesa can no
# longer be installed (finding from M3).
#
# --assume-installed: inside a seat the graphics driver always comes from the
# host, never from a package. Without these flags pacman picks the first
# provider of the virtual packages, which in the CachyOS repository is
# lib32-nvidia-390xx-utils, a ten year old driver that would overwrite exactly
# the injected files.
incus exec "$CT" -- sh -c 'grep -q "^\[multilib\]" /etc/pacman.conf || printf "\n[multilib]\nInclude = /etc/pacman.d/mirrorlist\n" >> /etc/pacman.conf'
incus exec "$CT" -- pacman -Sy --noconfirm >/dev/null
incus exec "$CT" -- pacman -S --noconfirm --needed \
    --assume-installed vulkan-driver \
    --assume-installed lib32-vulkan-driver \
    --assume-installed opengl-driver \
    steam lib32-libglvnd lib32-vulkan-icd-loader ttf-liberation zenity

step "Creating user '$PLAYER'"
incus exec "$CT" -- sh -c "
    id -u $PLAYER >/dev/null 2>&1 || useradd -m -s /bin/bash -G video,input,audio $PLAYER
    loginctl enable-linger $PLAYER
"

step "Enabling NVIDIA, passing through GPU, uinput and uhid"
incus config set "$CT" nvidia.runtime=true nvidia.driver.capabilities=all
incus config device add "$CT" gpu gpu
incus config device add "$CT" uinput unix-char \
    source=/dev/uinput path=/dev/uinput mode=0666 required=false
# /dev/uhid is NOT optional: Sunshine creates gamepads through inputtino as HID
# devices, not through uinput. Without uhid it happily logs "Gamepad 0 will be
# Xbox One controller" while no device ever appears inside the seat - the
# client's controller simply does nothing.
incus config device add "$CT" uhid unix-char \
    source=/dev/uhid path=/dev/uhid mode=0666 required=false
incus restart "$CT"
sleep 5

step "Checks"
incus exec "$CT" -- nvidia-smi -L | tee /dev/stderr | grep -q '^GPU' \
    && ok "GPU visible" || bad "GPU missing"
incus exec "$CT" -- sunshine --version 2>&1 | head -2 || warn "sunshine --version failed"
echo "  LAN address:"
incus exec "$CT" -- ip -4 -br addr show eth1

echo
echo "Next: ./15-nvidia-userspace.sh"
