#!/usr/bin/env bash
# Builds the .deb and the .rpm from this checkout.
#
# The Arch package is not built here. That one is makepkg's job and lives in
# packaging/aur/PKGBUILD, which builds from a release tarball rather than from a
# working tree; this builds the binary that is already in front of it. Both are
# run by .github/workflows/package.yml against the same tag, and packaging/README.md
# says which produces what.
#
# Runnable by hand, and meant to be: the release list asks for the result to be
# installed on a real machine before it is published, and a package that can
# only be produced by CI cannot be tested that way.
#
#   ./build-packages.sh              version from git describe
#   ./build-packages.sh 0.8.2        version said outright, which is what CI does
#
# The two files land in packaging/dist/ under the names the daemon looks for.
# They carry no version, on purpose and for the reason packaging/README.md gives:
# releases/latest/download/<name> is a permanent link only while <name> is, and
# both package managers read the real version out of the file rather than off it.
set -euo pipefail

HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd -- "$HERE/.." && pwd)"
BUILD="$HERE/build"
DIST="$HERE/dist"

# Pinned rather than @latest. This tool writes the packages that a machine
# installs as root, so which version of it ran is part of what a release is, and
# "whatever was newest that morning" is not an answer to that question.
NFPM_VERSION=${NFPM_VERSION:-v2.47.0}

ok()   { printf '  \033[32m✓\033[0m %s\n' "$*"; }
bad()  { printf '  \033[31m✗\033[0m %s\n' "$*"; }
warn() { printf '  \033[33m!\033[0m %s\n' "$*"; }
step() { printf '\n\033[1m%s\033[0m\n' "$*"; }

# The version, without the leading v. Debian and RPM both read a leading letter
# as part of the version string and then compare it as one, which makes v0.9.0
# older than 0.8.1 on a machine deciding whether an upgrade is an upgrade.
version=${1:-}

if [[ -z $version ]]; then
    version=$(git -C "$REPO" describe --tags --abbrev=0 2>/dev/null || true)
    [[ -n $version ]] || { echo "no tag to take a version from, so pass one"; exit 1; }
fi

version=${version#v}

if [[ ! $version =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    bad "\"$version\" is not MAJOR.MINOR.PATCH"
    exit 1
fi

step "nfpm"
# Which nfpm ran is part of what a release is, so this says so rather than
# leaving it to be assumed.
#
# `nfpm --version` cannot answer it: a binary from `go install` reports
# GitVersion: dev, because the real version is stamped by goreleaser at release
# time and not by the module path it was installed from. So the pin is what gets
# printed, and the one case where the pin is not what ran is called out rather
# than papered over.
if command -v nfpm >/dev/null 2>&1; then
    warn "using the nfpm already on PATH: $(command -v nfpm)"
    echo "    $NFPM_VERSION is what this script pins, and it is not what will run."
    echo "    Take that one off PATH for a build meant to be published."
else
    echo "  installing nfpm $NFPM_VERSION"
    GOFLAGS= go install "github.com/goreleaser/nfpm/v2/cmd/nfpm@$NFPM_VERSION"

    # go install puts it in GOBIN, which is not always on PATH.
    PATH="$(go env GOPATH)/bin:$PATH"
    export PATH

    command -v nfpm >/dev/null 2>&1 || { bad "nfpm is not on PATH after installing it"; exit 1; }
    ok "nfpm $NFPM_VERSION"
fi

step "Building polyseatd $version"
rm -rf "$BUILD" "$DIST"
mkdir -p "$BUILD" "$DIST"

# The same stamp the PKGBUILD applies, and it matters as much here: without it
# the daemon calls itself "dev", the interface shows that, and the update check
# refuses to run because a build that cannot name itself as a release cannot be
# compared with one.
( cd "$REPO" && go build \
    -trimpath \
    -ldflags "-X github.com/superuser404notfound/Polyseat/internal/version.Version=v$version" \
    -o "$BUILD/polyseatd" ./cmd/polyseatd )

ok "$("$BUILD/polyseatd" -version)"

step "Staging the unit"
# The unit in the tree points at /usr/local/bin, which is right for the checkout
# install that writes it there. A package owns /usr/bin. The PKGBUILD makes the
# same substitution for the same reason.
sed 's|/usr/local/bin/polyseatd|/usr/bin/polyseatd|' \
    "$REPO/host/polyseatd.service" > "$BUILD/polyseatd.service"

grep -q '/usr/bin/polyseatd' "$BUILD/polyseatd.service" ||
    { bad "the unit does not point at /usr/bin after rewriting"; exit 1; }

ok "polyseatd.service points at /usr/bin"

step "Packaging"
export POLYSEAT_VERSION=$version

# Named outright rather than left to nfpm's default, which carries the version.
# These two names are what internal/hostpkg's Asset says a host looks for, and
# a test holds the two against each other.
( cd "$REPO" && nfpm package -f packaging/nfpm.yaml -p deb -t "$DIST/polyseat_amd64.deb" )
ok "$(basename "$DIST/polyseat_amd64.deb") $(du -h "$DIST/polyseat_amd64.deb" | cut -f1)"

( cd "$REPO" && nfpm package -f packaging/nfpm.yaml -p rpm -t "$DIST/polyseat.x86_64.rpm" )
ok "$(basename "$DIST/polyseat.x86_64.rpm") $(du -h "$DIST/polyseat.x86_64.rpm" | cut -f1)"

step "Done"
echo "  $DIST"
