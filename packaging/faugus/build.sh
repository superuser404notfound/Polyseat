#!/bin/sh
# Polyseat - build the Faugus package a seat installs.
#
#   packaging/faugus/build.sh <container>
#
# Runs inside a running Arch container that Incus knows by that name, a seat for
# instance, and leaves the package next to this script. Not on the host, because
# makepkg wants Arch whatever the host is. A minute, most of it pacman.
#
# Faugus is Python and the package is arch=any, so nothing is compiled; meson
# only puts the files in place. The tools it needs are taken out again at the
# end if this script put them in.
set -eu

ct=${1:?usage: build.sh <container>}
here=$(cd "$(dirname "$0")" && pwd)
dir=/var/tmp/polyseat-faugus-build

say() { echo "build.sh: $*" >&2; }

tools="base-devel meson gettext"

added=
for p in $tools; do
    incus exec "$ct" -- pacman -Q "$p" >/dev/null 2>&1 || added="$added $p"
done

say "build dependencies"
# shellcheck disable=SC2086
incus exec "$ct" -- pacman -S --needed --noconfirm $tools

say "recipe into $ct:$dir"
incus exec "$ct" -- sh -c "rm -rf $dir && install -d -o nobody -g nobody $dir"
incus file push -q "$here/PKGBUILD" "$ct$dir/PKGBUILD"
incus exec "$ct" -- chown -R nobody:nobody "$dir"

say "building"
# makepkg refuses to run as root. --nodeps because the runtime dependencies
# are pacman's business when the package is installed, not this build's.
incus exec "$ct" -- runuser -u nobody -- env HOME="$dir" PKGEXT=.pkg.tar.zst \
    sh -c "cd $dir && makepkg --nodeps --noconfirm --clean"

pkg=$(incus exec "$ct" -- sh -c "ls $dir/faugus-launcher-*.pkg.tar.zst | head -1")
[ -n "$pkg" ] || { say "makepkg left no package behind"; exit 1; }

incus file pull -q "$ct$pkg" "$here/"
say "$(basename "$pkg")"
sha256sum "$here/$(basename "$pkg")"

if [ -n "$added" ]; then
    say "taking$added out again"
    # shellcheck disable=SC2086
    incus exec "$ct" -- pacman -Rns --noconfirm $added >/dev/null
fi
