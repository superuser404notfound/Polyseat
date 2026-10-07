#!/bin/sh
# Polyseat - build the Sunshine package a seat installs.
#
#   packaging/sunshine/build.sh <container>
#
# Runs inside a running Arch container that Incus knows by that name, a seat for
# instance, and leaves the package next to this script. Not on the host: the
# build wants the CUDA toolkit, which is five gigabytes that no host needs
# afterwards, and makepkg wants Arch whatever the host is.
#
# Four compilers at a time and no more. Twenty-four of them took a 32 GB host
# with a virtual machine on it to the point where it started killing things, on
# 2026-10-07, and a build that takes a quarter of an hour longer is the better
# trade.
#
# The toolkit is taken out again at the end if this script put it in.
set -eu

ct=${1:?usage: build.sh <container>}
here=$(cd "$(dirname "$0")" && pwd)
dir=/var/tmp/polyseat-sunshine-build

say() { echo "build.sh: $*" >&2; }

# The driver in a seat is a set of files mirrored in from the host and not a
# package, so anything that resolves a dependency on it has to be told so. Same
# flags and same reason as in provision.go.
assume="--assume-installed opengl-driver --assume-installed vulkan-driver
        --assume-installed lib32-opengl-driver --assume-installed lib32-vulkan-driver
        --assume-installed opencl-nvidia"

had_cuda=1
incus exec "$ct" -- pacman -Q cuda >/dev/null 2>&1 || had_cuda=0

say "build dependencies"
# shellcheck disable=SC2086
incus exec "$ct" -- pacman -S --needed --noconfirm $assume \
    base-devel appstream appstream-glib cmake cuda desktop-file-utils gcc git \
    make nodejs npm python-jinja shaderc \
    avahi curl gtk3 libayatana-appindicator libcap libdrm libevdev libmfx \
    libpipewire libpulse libva libx11 libxcb libxfixes libxrandr libxtst \
    miniupnpc numactl openssl opus qt6-base qt6-svg vulkan-icd-loader which

say "recipe into $ct:$dir"
incus exec "$ct" -- sh -c "rm -rf $dir && install -d -o nobody -g nobody $dir"
for f in PKGBUILD sunshine.install wlgrab-event-driven.patch; do
    incus file push -q "$here/$f" "$ct$dir/$f"
done
incus exec "$ct" -- chown -R nobody:nobody "$dir"

say "building, this is the long part"
# makepkg refuses to run as root. --nodeps because the dependencies are in and
# makepkg would otherwise ask pacman, which trips over the driver again.
incus exec "$ct" -- runuser -u nobody -- env HOME="$dir" MAKEFLAGS=-j4 PKGEXT=.pkg.tar.zst \
    sh -c "cd $dir && makepkg --nodeps --noconfirm --clean"

pkg=$(incus exec "$ct" -- sh -c "ls $dir/sunshine-*.pkg.tar.zst | grep -v -- -debug | head -1")
[ -n "$pkg" ] || { say "makepkg left no package behind"; exit 1; }

incus file pull -q "$ct$pkg" "$here/"
say "$(basename "$pkg")"
sha256sum "$here/$(basename "$pkg")"

if [ "$had_cuda" = 0 ]; then
    say "taking the CUDA toolkit out again"
    incus exec "$ct" -- pacman -Rns --noconfirm cuda >/dev/null
fi
