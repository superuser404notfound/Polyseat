#!/usr/bin/env bash
# Legt den vollständigen Seat-Container an — bis vor den Start der Session.
#
# Reihenfolge ist bewusst: Pakete VOR dem Einschalten von nvidia.runtime,
# sonst kollidiert pacman mit den injizierten Treiberdateien (Befund aus M0).
set -euo pipefail
source "$(dirname "$0")/lib.sh"

PLAYER="${PLAYER:-player}"
UPLINK="${UPLINK:-enp4s0}"

if incus info "$CT" >/dev/null 2>&1; then
    warn "Container '$CT' existiert bereits — 99-cleanup.sh zuerst."
    exit 1
fi

wait_boot() {
    # `incus launch` kehrt zurück, sobald der Container läuft — nicht, sobald
    # dessen systemd bereit ist. Ohne diese Wartezeit scheitert das erste
    # systemctl mit "Failed to connect to system scope bus".
    for _ in $(seq 90); do
        incus exec "$CT" -- systemctl is-system-running 2>/dev/null \
            | grep -qE 'running|degraded' && return 0
        sleep 1
    done
    bad "systemd im Container wurde nicht bereit"
    return 1
}

step "Container '$CT' anlegen (noch ohne NVIDIA)"
incus launch images:archlinux/current "$CT"
wait_boot

step "Zweite Netzwerkkarte: macvlan ins LAN"
# eth0 bleibt an incusbr0 und ist der Verwaltungsweg (der Daemon spricht den
# Seat später darüber an). eth1 hängt per macvlan direkt im LAN, damit
# Moonlight den Seat als eigenständigen Host sieht — jeder Seat bekommt so
# eine eigene Adresse und kann die Standard-Sunshine-Ports benutzen.
# Bekannte Eigenschaft von macvlan: Host und Container können sich über
# dieses Interface NICHT direkt erreichen. Genau dafür bleibt eth0.
incus config device add "$CT" eth1 nic nictype=macvlan parent="$UPLINK" name=eth1

step "DHCP für eth1 im Container einrichten"
incus exec "$CT" -- sh -c 'cat > /etc/systemd/network/50-eth1.network <<EOF
[Match]
Name=eth1

[Network]
DHCP=yes
EOF
systemctl restart systemd-networkd'

step "Warten bis Netzwerk steht"
for _ in $(seq 60); do
    incus exec "$CT" -- getent hosts geo.mirror.pkgbuild.com >/dev/null 2>&1 && break
    sleep 1
done

step "CachyOS-Repo im Container bereitstellen"
# Sunshine liegt nicht in den Arch-Repos, sondern im CachyOS-Repo. Statt es
# aus dem AUR zu bauen, wird nur das *nicht* CPU-optimierte [cachyos]-Repo
# eingebunden — und zwar ANS ENDE der pacman.conf, damit Arch bei allen
# gemeinsamen Paketen gewinnt und der Container ein Arch-Container bleibt.
for p in cachyos-keyring cachyos-mirrorlist; do
    f=$(ls -t /var/cache/pacman/pkg/$p-*.pkg.tar.zst | head -1)
    incus file push "$f" "$CT/root/$(basename "$f")"
    incus exec "$CT" -- pacman -U --noconfirm "/root/$(basename "$f")"
done
# Die vom Paket gelieferte Mirrorliste beginnt mit Mirrors, die veraltete
# Indizes liefern; die bereits sortierte Liste des Hosts ist verlässlicher.
incus file push /etc/pacman.d/cachyos-mirrorlist "$CT/etc/pacman.d/cachyos-mirrorlist"
incus exec "$CT" -- sh -c 'grep -q "^\[cachyos\]" /etc/pacman.conf || printf "\n[cachyos]\nInclude = /etc/pacman.d/cachyos-mirrorlist\n" >> /etc/pacman.conf'
incus exec "$CT" -- pacman-key --populate cachyos

step "Pakete installieren"
incus exec "$CT" -- pacman -Syu --noconfirm --needed \
    sway swaybg foot xorg-xwayland \
    sunshine avahi \
    pipewire pipewire-pulse pipewire-audio wireplumber \
    mesa vulkan-tools mesa-utils \
    sudo which

step "Benutzer '$PLAYER' anlegen"
incus exec "$CT" -- sh -c "
    id -u $PLAYER >/dev/null 2>&1 || useradd -m -s /bin/bash -G video,input,audio $PLAYER
    loginctl enable-linger $PLAYER
"

step "NVIDIA einschalten, GPU und uinput durchreichen"
incus config set "$CT" nvidia.runtime=true nvidia.driver.capabilities=all
incus config device add "$CT" gpu gpu
incus config device add "$CT" uinput unix-char \
    source=/dev/uinput path=/dev/uinput required=false
incus restart "$CT"
sleep 5

step "Prüfen"
incus exec "$CT" -- nvidia-smi -L | tee /dev/stderr | grep -q '^GPU' \
    && ok "GPU sichtbar" || bad "GPU fehlt"
incus exec "$CT" -- sunshine --version 2>&1 | head -2 || warn "sunshine --version schlug fehl"
echo "  LAN-Adresse:"
incus exec "$CT" -- ip -4 -br addr show eth1

echo
echo "Weiter mit: ./15-nvidia-userspace.sh"
